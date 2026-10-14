package store

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestChangePasswordRevokesAPIKeysInSameTransaction(t *testing.T) {
	db := isolatedTestStore(t)
	ctx := t.Context()
	var userID int64
	hash, err := passwordHashForTest("integration-password")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, "INSERT INTO users(username,email,password_hash,display_name,role) VALUES('rotate-customer','rotate-customer@example.test',$1,'Rotate customer','customer') RETURNING id", hash).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	var otherID int64
	if err = db.Pool.QueryRow(ctx, "INSERT INTO users(username,email,password_hash,display_name,role) VALUES('rotate-other','rotate-other@example.test',$1,'Rotate other','customer') RETURNING id", hash).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	var otherKey int64
	insert := func(owner int64, prefix, name, secret string) int64 {
		t.Helper()
		var id int64
		if err := db.Pool.QueryRow(ctx, "INSERT INTO api_keys(user_id,name,prefix,secret_hash) VALUES($1,$2,$3,$4) RETURNING id", owner, name, prefix, secret).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	activeKey := insert(userID, "zjmfgo_0123456789abcdef", "active", strings.Repeat("a", 64))
	revokedKey := insert(userID, "zjmfgo_0123456789abcdee", "already revoked", strings.Repeat("b", 64))
	if _, err = db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", revokedKey); err != nil {
		t.Fatal(err)
	}
	otherKey = insert(otherID, "zjmfgo_0123456789abcdff", "other", strings.Repeat("c", 64))
	if _, err = db.Pool.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(repeat('1',64),$1,now()+interval '1 hour'),(repeat('2',64),$1,now()+interval '1 hour')", userID); err != nil {
		t.Fatal(err)
	}

	if err = db.ChangePassword(ctx, userID, strings.Repeat("1", 64), "integration-password", "rotated-integration-password"); err != nil {
		t.Fatal(err)
	}
	// Active keys revoke, already-revoked keys keep their original timestamp.
	var activeRevoked, revokedAt interface{}
	if err = db.Pool.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM api_keys WHERE id=$1", activeKey).Scan(&activeRevoked); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, "SELECT revoked_at FROM api_keys WHERE id=$1", revokedKey).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	var otherRevoked bool
	if err = db.Pool.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM api_keys WHERE id=$1", otherKey).Scan(&otherRevoked); err != nil {
		t.Fatal(err)
	}
	if activeRevoked != true || revokedAt == nil || otherRevoked {
		t.Fatalf("revocation scope wrong: active=%v alreadyRevokedAt=%v other=%v", activeRevoked, revokedAt, otherRevoked)
	}
	var sessions int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE user_id=$1", userID).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("session handling wrong: count=%d err=%v", sessions, err)
	}

	// A wrong current password must not revoke anything.
	var secondKey int64
	secondKey = insert(userID, "zjmfgo_0123456789abcd00", "second", strings.Repeat("d", 64))
	if err = db.ChangePassword(ctx, userID, strings.Repeat("2", 64), "wrong-password", "another-integration-password"); err == nil {
		t.Fatal("wrong current password accepted")
	}
	var secondRevoked bool
	if err = db.Pool.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM api_keys WHERE id=$1", secondKey).Scan(&secondRevoked); err != nil || secondRevoked {
		t.Fatalf("failed password change revoked keys: %v %v", secondRevoked, err)
	}
}

func passwordHashForTest(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	return string(hash), err
}
