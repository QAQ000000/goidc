package g09fixture

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (v *CohortVerifier) notificationIdentities(ctx context.Context, tx pgx.Tx) error {
	ids := make([]int64, 0, 150)
	for _, r := range v.cohort.sources {
		if r.EventID > 0 {
			ids = append(ids, r.EventID)
		}
	}
	rows, err := tx.Query(ctx, `SELECT channel,count(*),count(DISTINCT normalized_address),count(DISTINCT user_id),count(DISTINCT event_id) FROM `+v.table("ticket_notification_deliveries")+` WHERE event_id=ANY($1::bigint[]) GROUP BY channel ORDER BY channel LIMIT 3`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var channel string
		var total, addresses, staff, events int64
		if err = rows.Scan(&channel, &total, &addresses, &staff, &events); err != nil {
			return err
		}
		if (channel != "email" && channel != "sms") || total != 150 || addresses != 150 || staff != 150 || events != 150 {
			return errors.New("notification_identity_denominator")
		}
		n++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if n != 2 {
		return errors.New("notification_channel_denominator")
	}
	return nil
}

func (v *CohortVerifier) ticket(ctx context.Context, tx pgx.Tx, r CohortSourceRecord, now time.Time) ([]CohortTaskObservation, error) {
	var created, eventAt time.Time
	var eventN, deliveryN int64
	var status string
	err := tx.QueryRow(ctx, `SELECT t.status,t.created_at,e.created_at,
 (SELECT count(*) FROM `+v.table("ticket_notification_events")+` x WHERE x.ticket_id=t.id),
 (SELECT count(*) FROM `+v.table("ticket_notification_deliveries")+` x WHERE x.event_id=e.id)
 FROM `+v.table("tickets")+` t JOIN `+v.table("ticket_notification_events")+` e ON e.ticket_id=t.id AND e.id=$4 AND e.actor_id=$2 AND e.kind='customer_created' AND e.reply_id IS NULL
 WHERE t.id=$1 AND t.user_id=$2 AND t.department_id=$3`, r.TicketID, r.CustomerID, r.DepartmentID, r.EventID).Scan(&status, &created, &eventAt, &eventN, &deliveryN)
	if err != nil {
		return nil, err
	}
	if status != "open" || eventN != 1 || deliveryN != 2 || created.Before(v.baseline.CapturedAtUTC) || created.After(r.AcceptedAtUTC) || eventAt.Before(created) || eventAt.After(r.AcceptedAtUTC) {
		return nil, errors.New("ticket_source_mapping")
	}
	tasks := make([]CohortTaskObservation, 0, 2)
	for _, channel := range []string{"email", "sms"} {
		did, oid := r.EmailDeliveryID, r.EmailOutboxID
		column, other := "email_outbox_id", "sms_outbox_id"
		if channel == "sms" {
			did, oid = r.SMSDeliveryID, r.SMSOutboxID
			column, other = other, column
		}
		addressColumn := "email"
		if channel == "sms" {
			addressColumn = "phone"
		}
		task := CohortTaskObservation{FlowID: r.FlowID, Kind: channel, ID: oid}
		var eligible bool
		var deliveryAt time.Time
		q := `SELECT o.status,o.attempts,o.max_attempts,o.created_at,o.available_at,o.locked_at,o.sent_at,o.expires_at,d.created_at,
 (d.preference_enabled AND d.preference_version=p.version AND d.address_verified AND d.reason='' AND d.duplicate_of IS NULL AND d.template_kind='ticket_customer_created' AND d.` + other + ` IS NULL AND o.recipient=d.normalized_address AND d.normalized_address=lower(btrim(u.` + addressColumn + `)) AND o.kind='ticket_customer_created' AND p.email_enabled AND p.sms_enabled AND u.email_verified AND u.phone_verified AND u.status='active' AND u.role IN ('admin','super_admin'))
 FROM ` + v.table("ticket_notification_deliveries") + ` d
 JOIN ` + v.table(channel+"_outbox") + ` o ON o.id=d.` + column + ` AND o.id=$3
 JOIN ` + v.table("users") + ` u ON u.id=d.user_id AND u.id=$4
 JOIN ` + v.table("user_ticket_notification_preferences") + ` p ON p.user_id=u.id
 WHERE d.id=$1 AND d.event_id=$2 AND d.customer_id=$5 AND d.recipient_kind='user' AND d.recipient_id=$4 AND d.contact_id IS NULL AND d.channel=$6 AND d.outcome='queued'`
		err = tx.QueryRow(ctx, q, did, r.EventID, oid, r.StaffID, r.CustomerID, channel).Scan(&task.Status, &task.Attempts, &task.MaxAttempts, &task.CreatedAt, &task.AvailableAt, &task.LockedAt, &task.FinishedAt, &task.ExpiresAt, &deliveryAt, &eligible)
		if err != nil {
			return nil, err
		}
		if !eligible || deliveryAt.Before(eventAt) || deliveryAt.After(r.AcceptedAtUTC) || task.CreatedAt.Before(eventAt) || task.CreatedAt.After(r.AcceptedAtUTC) {
			return nil, errors.New("ticket_delivery_mapping")
		}
		if err = checkCohortTask(task, now, true); err != nil {
			return nil, err
		}
		if channel == "sms" && task.Status == "sent" {
			var n, accepted int64
			err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE attempts=$2 AND outcome='accepted' AND http_status BETWEEN 200 AND 299 AND locked_at>= $3 AND created_at>=locked_at AND created_at<=$4) FROM `+v.table("sms_delivery_evidence")+` WHERE outbox_id=$1`, oid, task.Attempts, task.CreatedAt, *task.FinishedAt).Scan(&n, &accepted)
			if err != nil {
				return nil, err
			}
			if n != 1 || accepted != 1 {
				return nil, errors.New("sms_database_evidence")
			}
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func (v *CohortVerifier) finance(ctx context.Context, tx pgx.Tx) error {
	for uid, base := range v.baseline.Customers {
		var balance, creditLimit, credit int64
		// A missing optional credit account has no credit facility. This is a known
		// zero under the product contract, not a failed/unavailable database read.
		err := tx.QueryRow(ctx, `SELECT u.balance_cents,coalesce(c.limit_cents,0),coalesce(c.used_cents,0) FROM `+v.table("users")+` u LEFT JOIN `+v.table("customer_credit_accounts")+` c ON c.user_id=u.id WHERE u.id=$1 AND u.role='customer' AND u.status='active'`, uid).Scan(&balance, &creditLimit, &credit)
		if err != nil {
			return err
		}
		if creditLimit != 0 || credit != 0 {
			return errors.New("credit_used")
		}
		invoices := map[int64]bool{}
		for _, r := range v.cohort.sources {
			if r.CustomerID == uid && r.InvoiceID > 0 {
				invoices[r.InvoiceID] = true
			}
		}
		// One extra row detects unexpected ledger activity without unbounded output.
		rows, err := tx.Query(ctx, `SELECT id,entry_type,delta_cents,balance_after,reference_type,reference_id FROM `+v.table("balance_ledger")+` WHERE user_id=$1 ORDER BY id LIMIT $2`, uid, len(base.Ledger)+len(invoices)+1)
		if err != nil {
			return err
		}
		i := 0
		closing := base.BalanceCents
		paid := map[int64]bool{}
		for rows.Next() {
			var id, delta, after int64
			var typ, ref string
			var refID *int64
			if err = rows.Scan(&id, &typ, &delta, &after, &ref, &refID); err != nil {
				rows.Close()
				return err
			}
			if i < len(base.Ledger) {
				l := base.Ledger[i]
				if id != l.ID || delta != l.DeltaCents || after != l.BalanceAfter {
					rows.Close()
					return errors.New("ledger_checkpoint_changed")
				}
			} else {
				if typ != "payment" || ref != "invoice" || refID == nil || !invoices[*refID] || paid[*refID] || delta != -1000 || closing < 1000 {
					rows.Close()
					return errors.New("ledger_new_source_invalid")
				}
				closing -= 1000
				if after != closing {
					rows.Close()
					return errors.New("ledger_balance_chain")
				}
				paid[*refID] = true
			}
			i++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if i != len(base.Ledger)+len(invoices) || len(paid) != len(invoices) || balance != closing {
			return errors.New("balance_ledger_conservation")
		}
	}
	return nil
}

func (v *CohortVerifier) inventory(ctx context.Context, tx pgx.Tx) error {
	for pid, base := range v.baseline.Products {
		var stock *int64
		if err := tx.QueryRow(ctx, `SELECT stock FROM `+v.table("products")+` WHERE id=$1`, pid).Scan(&stock); err != nil {
			return err
		}
		added := map[int64]bool{}
		for _, r := range v.cohort.sources {
			if r.ProductID == pid {
				added[r.ServiceID] = true
			}
		}
		rows, err := tx.Query(ctx, `SELECT id,inventory_released FROM `+v.table("services")+` WHERE product_id=$1 ORDER BY id LIMIT $2`, pid, len(base.Services)+len(added)+1)
		if err != nil {
			return err
		}
		seen := map[int64]bool{}
		var released int64
		for rows.Next() {
			var sid int64
			var release bool
			if err = rows.Scan(&sid, &release); err != nil {
				rows.Close()
				return err
			}
			was, old := base.Services[sid]
			if (!old && !added[sid]) || (old && was && !release) || (!old && release) {
				rows.Close()
				return errors.New("stock_service_mapping")
			}
			if old && !was && release {
				released++
			}
			seen[sid] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(seen) != len(base.Services)+len(added) {
			return errors.New("stock_service_missing")
		}
		if (stock == nil) != (base.Stock == nil) {
			return errors.New("stock_nullability_changed")
		}
		if stock != nil && *stock != *base.Stock-int64(len(added))+released {
			return errors.New("stock_conservation")
		}
	}
	return nil
}
