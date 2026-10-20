package store

import (
	"context"
	"errors"
	"net/mail"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type AccountVerificationInput struct {
	Flow, Purpose, Channel, Recipient, Identifier, Portal string
	ActorID                                               int64
	SessionHash                                           string
}
type AccountVerificationResult struct {
	ID, Denial string
}

func verificationRecipientValid(channel, recipient string) bool {
	if channel == "sms" {
		return ValidSMSRecipient(recipient)
	}
	address, err := mail.ParseAddress(recipient)
	return channel == "email" && err == nil && address.Address == recipient && len(recipient) <= 254
}

func (s *Store) IssueAccountVerification(ctx context.Context, in AccountVerificationInput) (out AccountVerificationResult, err error) {
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		var uid *int64
		deliver := true
		if in.Flow == "register" {
			var enabled bool
			if err := tx.QueryRow(ctx, "SELECT s.registration_enabled AND a.registration_email_enabled FROM site_settings s JOIN auth_settings a ON a.id=1 WHERE s.id=1 FOR SHARE").Scan(&enabled); err != nil {
				return err
			}
			if !enabled {
				out.Denial = "registration_disabled"
				return nil
			}
		} else if in.Flow == "password_reset" {
			// Resolve one physical owner before taking its row lock. An omitted
			// portal never chooses between colliding customer/admin identifiers.
			tables := []string{"users", "administrators"}
			if in.Portal != "" {
				table, err := authenticationTableForPortal(in.Portal)
				if err != nil {
					return err
				}
				tables = []string{table}
			}
			var owner int64
			var ownerTable string
			matches := 0
			for _, table := range tables {
				var candidate int64
				err := tx.QueryRow(ctx, "SELECT id FROM "+table+" WHERE (lower(username)=$1 OR lower(email)=$1 OR phone=$1) AND status='active'", in.Identifier).Scan(&candidate)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if err == nil {
					matches++
					owner = candidate
					ownerTable = table
				}
			}
			var email, phone string
			var phoneVerified bool
			if matches == 1 {
				err := tx.QueryRow(ctx, "SELECT id,email,phone,phone_verified FROM "+ownerTable+" WHERE id=$2 AND (lower(username)=$1 OR lower(email)=$1 OR phone=$1) AND status='active' FOR UPDATE", in.Identifier, owner).Scan(&owner, &email, &phone, &phoneVerified)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if errors.Is(err, pgx.ErrNoRows) {
					matches = 0
				}
			}
			if matches != 1 {
				deliver = false
				in.Recipient = "unknown:" + in.Identifier
			} else {
				uid = &owner
				if in.Channel == "email" {
					in.Recipient = email
				} else {
					in.Recipient = phone
					deliver = phoneVerified && phone != ""
					if !deliver {
						in.Recipient = "unknown:" + in.Identifier
					}
				}
			}
		} else {
			table, err := authenticationTable(ctx, tx, in.ActorID)
			if err != nil {
				return err
			}
			var active bool
			if err = tx.QueryRow(ctx, "SELECT status='active' FROM "+table+" WHERE id=$1 FOR UPDATE", in.ActorID).Scan(&active); err != nil {
				return err
			}
			if !active {
				out.Denial = "unauthorized"
				return nil
			}
			if err = lockAccountSession(ctx, tx, in.ActorID, in.SessionHash); err != nil {
				return err
			}
			uid = &in.ActorID
		}
		if deliver && !verificationRecipientValid(in.Channel, in.Recipient) {
			out.Denial = "invalid_verification_recipient"
			return nil
		}
		id, err := s.IssueVerification(ctx, tx, in.Purpose, in.Channel, in.Recipient, uid, deliver)
		if err != nil {
			return err
		}
		out.ID = id
		if uid != nil {
			action := "auth.verification_requested"
			if in.Purpose == "password_reset" {
				action = "auth.password_reset_requested"
			}
			return Audit(ctx, tx, *uid, action, "user", *uid, map[string]any{"purpose": in.Purpose, "channel": in.Channel})
		}
		return nil
	})
	return
}

func (s *Store) ConfirmAccountPasswordReset(ctx context.Context, challengeID, code, password string) (valid bool, err error) {
	var uid *int64
	var recipient, channel string
	err = s.Pool.QueryRow(ctx, "SELECT user_id,recipient,channel FROM verification_challenges WHERE id=$1 AND purpose='password_reset'", VerificationID(challengeID)).Scan(&uid, &recipient, &channel)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && uid == nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return false, err
	}
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		table, err := authenticationTable(ctx, tx, *uid)
		if err != nil {
			return err
		}
		var status, email, phone string
		var phoneVerified bool
		if err = tx.QueryRow(ctx, "SELECT status,email,phone,phone_verified FROM "+table+" WHERE id=$1 FOR UPDATE", *uid).Scan(&status, &email, &phone, &phoneVerified); err != nil {
			return err
		}
		_, accepted, err := s.ConsumeVerification(ctx, tx, challengeID, code, "password_reset", uid)
		if err != nil {
			return err
		}
		if !accepted || status != "active" || channel == "email" && email != recipient || channel == "sms" && (!phoneVerified || phone != recipient) {
			return nil
		}
		if _, err = tx.Exec(ctx, "UPDATE "+table+" SET password_hash=$2,password_reset_required=false,admin_version=admin_version+1,updated_at=now() WHERE id=$1", *uid, string(hash)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", *uid); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM mfa_challenges WHERE user_id=$1", *uid); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL", *uid); err != nil {
			return err
		}
		if err = Audit(ctx, tx, *uid, "auth.password_reset_completed", "user", *uid, map[string]any{"channel": channel}); err != nil {
			return err
		}
		valid = true
		return nil
	})
	return
}

func (s *Store) ConfirmAccountContactVerification(ctx context.Context, uid int64, sessionHash, challengeID, code string) (valid bool, err error) {
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		table, err := authenticationTable(ctx, tx, uid)
		if err != nil {
			return err
		}
		var email, status string
		if err = tx.QueryRow(ctx, "SELECT email,status FROM "+table+" WHERE id=$1 FOR UPDATE", uid).Scan(&email, &status); err != nil {
			return err
		}
		if err = lockAccountSession(ctx, tx, uid, sessionHash); err != nil {
			return err
		}
		var purpose string
		err = tx.QueryRow(ctx, "SELECT purpose FROM verification_challenges WHERE id=$1", VerificationID(challengeID)).Scan(&purpose)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if purpose != "bind_email" && purpose != "bind_phone" {
			return nil
		}
		challenge, accepted, err := s.ConsumeVerification(ctx, tx, challengeID, code, purpose, &uid)
		if err != nil {
			return err
		}
		if !accepted || status != "active" || purpose == "bind_email" && challenge.Recipient != email {
			return nil
		}
		if purpose == "bind_email" {
			_, err = tx.Exec(ctx, "UPDATE "+table+" SET email_verified=true WHERE id=$1", uid)
		} else {
			_, err = tx.Exec(ctx, "UPDATE "+table+" SET phone=$2 WHERE id=$1", uid, challenge.Recipient)
			if err == nil {
				_, err = tx.Exec(ctx, "UPDATE "+table+" SET phone_verified=true WHERE id=$1", uid)
			}
		}
		if err != nil {
			return err
		}
		if err = Audit(ctx, tx, uid, "auth.contact_verified", "user", uid, map[string]any{"channel": challenge.Channel}); err != nil {
			return err
		}
		valid = true
		return nil
	})
	return
}
