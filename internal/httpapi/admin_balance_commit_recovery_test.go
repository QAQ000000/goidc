package httpapi

import (
	"io"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAdminBalanceOperationRecoversLostCommitAcknowledgement(t *testing.T) {
	f := newAdminBalanceFixture(t)
	var commits atomic.Int32
	cfg := f.db.Pool.Config().Copy()
	cfg.ConnConfig.BuildFrontend = func(reader io.Reader, writer io.Writer) *pgproto3.Frontend {
		// Reuse the wire-level fixture: PostgreSQL actually commits, while
		// the caller loses its CommandComplete acknowledgement and sees EOF.
		return pgproto3.NewFrontend(&uploadCommitAckReader{reader: reader, count: &commits, dropAt: 1}, writer)
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	uncertain := &store.Store{Pool: pool}
	defer uncertain.Close()
	ctx := store.WithStorageCredential(t.Context(), tokenHash(cookieValue(f.cookie)), false)
	const key = "balance-operation-lost-commit"
	_, _, err = uncertain.TopUp(ctx, f.actor, f.customer, 1700, key, "original intention")
	if err == nil || commits.Load() != 1 {
		t.Fatal("fixture did not lose the real commit acknowledgement", err, commits.Load())
	}
	// Recovery uses the normal API and original key; it must not credit again.
	result := f.call("GET", key, nil, "")
	expect(t, result, 200)
	got := data(t, result)
	if got["state"] != "confirmed" || got["user_id"] != float64(f.customer) || got["amount_cents"] != float64(1700) || got["balance_cents"] != float64(1700) {
		t.Fatal("durable credit was not recovered", result.Body.String())
	}
	f.assertFunds(t, 1700, 1, 1)
	replay := f.call("POST", key, map[string]any{"amount_cents": 1700, "note": "original intention"}, "")
	expect(t, replay, 200)
	if data(t, replay)["replayed"] != true {
		t.Fatal("same-key recovery created a new credit", replay.Body.String())
	}
	f.assertFunds(t, 1700, 1, 1)
}
