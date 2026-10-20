package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

type AccountBusinessError struct {
	Status        int
	Code, Message string
}

func (e *AccountBusinessError) Error() string { return e.Code + ": " + e.Message }

func accountBusinessHash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func loadAccountBusinessReplay(ctx context.Context, tx pgx.Tx, actor int64, operation, key, hash string) (map[string]any, bool, error) {
	var out map[string]any
	replayed, err := ReplayOperation(ctx, tx, actor, "admin:"+operation, key, hash, &out)
	if err == nil && replayed {
		out["replayed"] = true
	}
	return out, replayed, err
}
