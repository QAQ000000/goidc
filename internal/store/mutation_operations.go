package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ReplayOperation serializes a receipt's actor/scope/key, including its first
// insertion. Callers must authorize the actor and resource before replaying;
// the lock and any saved receipt live in the same transaction as the mutation.
// Scopes preserve the old tables' key domains (not individual API actions).
func ReplayOperation(ctx context.Context, tx pgx.Tx, actor int64, scope, key, hash string, out any) (bool, error) {
	if err := lockOperationKey(ctx, tx, "mutation", fmt.Sprintf("%d:%s:%s", actor, scope, key)); err != nil {
		return false, err
	}
	var saved string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT request_hash,result FROM mutation_operations
 WHERE actor_id=$1 AND scope=$2 AND idempotency_key=$3 FOR UPDATE`, actor, scope, key).Scan(&saved, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if saved != hash {
		return false, conflict("idempotency_conflict", "相同幂等键对应不同请求")
	}
	if out != nil {
		if err = json.Unmarshal(raw, out); err != nil {
			return false, err
		}
	}
	return true, nil
}

// SaveOperation deliberately rejects duplicate inserts; ReplayOperation must be
// called first. Never overwrite a committed result on an idempotency conflict.
func SaveOperation(ctx context.Context, tx pgx.Tx, actor int64, scope, key, hash string, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO mutation_operations(actor_id,scope,idempotency_key,request_hash,result)
 VALUES($1,$2,$3,$4,$5)`, actor, scope, key, hash, raw)
	return err
}
