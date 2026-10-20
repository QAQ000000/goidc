package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const adminRenewalBatchScope = "admin:service.renewal.batch"
const adminRenewalBatchLimit = 20

// Coordinators use separate, short-lived database connections so concurrent
// batches cannot occupy every business-pool connection while waiting for one.
// Bound their number per process instead of opening an unbounded second pool.
var adminRenewalBatchSlots = make(chan struct{}, 4)

type AdminRenewalBatchQuoteItem struct {
	ServiceID       int64 `json:"service_id"`
	ExpectedVersion int64 `json:"expected_version"`
}

type AdminRenewalBatchQuoteInput struct {
	CustomerID int64                        `json:"customer_id"`
	Items      []AdminRenewalBatchQuoteItem `json:"items"`
}

type AdminRenewalBatchItemInput struct {
	ServiceID       int64  `json:"service_id"`
	ExpectedVersion int64  `json:"expected_version"`
	QuoteHash       string `json:"quote_hash,omitempty"`
	InvoiceID       int64  `json:"invoice_id,omitempty"`
	AmountCents     *int64 `json:"amount_cents,omitempty"`
}

type AdminRenewalBatchInput struct {
	CustomerID int64                        `json:"customer_id"`
	Action     string                       `json:"action"`
	Reason     string                       `json:"reason"`
	Items      []AdminRenewalBatchItemInput `json:"items"`
}

type adminRenewalBatchManifest struct {
	BatchID string `json:"batch_id"`
	AdminRenewalBatchInput
}

type AdminRenewalBatchExecutionInput struct {
	ServiceIDs []int64 `json:"service_ids"`
}

type AdminRenewalBatchItem struct {
	AdminRenewalBatchItemInput
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Code   string          `json:"code,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type AdminRenewalBatchResult struct {
	BatchID     string                  `json:"batch_id"`
	CustomerID  int64                   `json:"customer_id"`
	Action      string                  `json:"action"`
	Reason      string                  `json:"reason"`
	Items       []AdminRenewalBatchItem `json:"items"`
	Succeeded   int                     `json:"succeeded"`
	Failed      int                     `json:"failed"`
	Unconfirmed int                     `json:"unconfirmed"`
	Pending     int                     `json:"pending"`
	Replayed    bool                    `json:"replayed"`
}

type adminRenewalBatchAttempt struct {
	ServiceIDs []int64 `json:"service_ids"`
}

type adminRenewalBatchOutcome struct {
	ServiceID int64  `json:"service_id"`
	Code      string `json:"code"`
	Error     string `json:"error"`
}

// Batch membership is rechecked in the individual command's transaction too.
// A preflight alone must never let a transferred service spend another wallet.
type adminRenewalBatchCustomerKey struct{}

func requireAdminRenewalBatchCustomer(ctx context.Context, customer int64) error {
	if expected, ok := ctx.Value(adminRenewalBatchCustomerKey{}).(int64); ok && expected != customer {
		return pgx.ErrNoRows
	}
	return nil
}

func validRenewalBatchID(id string) bool {
	raw, err := hex.DecodeString(id)
	return err == nil && len(raw) == 32 && id == strings.ToLower(id)
}

func renewalBatchInvalid(message string) error {
	return &AccountBusinessError{400, "invalid_renewal_batch", message}
}

func validateRenewalBatchInput(in *AdminRenewalBatchInput) error {
	in.Reason = strings.TrimSpace(in.Reason)
	limit := 240
	if in.Action == "pay_balance" {
		limit = 500
	} else if in.Action != "create" {
		return renewalBatchInvalid("请选择建单或余额付款，不能合并执行")
	}
	if in.CustomerID < 1 || len(in.Items) < 1 || len(in.Items) > adminRenewalBatchLimit || in.Reason == "" || !contentTextValid(in.Reason, limit, true) {
		return renewalBatchInvalid("请提供客户、1至20项服务和有效原因")
	}
	services, invoices := map[int64]bool{}, map[int64]bool{}
	for _, item := range in.Items {
		if item.ServiceID < 1 || item.ExpectedVersion < 1 || services[item.ServiceID] {
			return renewalBatchInvalid("服务与版本必须有效，服务不能重复")
		}
		services[item.ServiceID] = true
		if in.Action == "create" {
			if !validRenewalBatchID(item.QuoteHash) || item.InvoiceID != 0 || item.AmountCents != nil {
				return renewalBatchInvalid("建单只能提交服务版本及有效报价摘要")
			}
		} else {
			if item.InvoiceID < 1 || invoices[item.InvoiceID] || item.AmountCents == nil || *item.AmountCents < 0 || item.QuoteHash != "" {
				return renewalBatchInvalid("付款必须逐项确认账单、版本和金额，账单不能重复")
			}
			invoices[item.InvoiceID] = true
		}
	}
	return nil
}

func adminRenewalBatchPermission(action string) string {
	if action == "pay_balance" {
		return "services.renew.pay_balance"
	}
	return "services.renew"
}

// All membership locks are short lived and follow invoice -> service order.
// No parent transaction remains open while a child command or supplier runs.
func authorizeAdminRenewalBatch(ctx context.Context, tx pgx.Tx, actor int64, in AdminRenewalBatchInput) (bool, error) {
	return authorizeAdminRenewalBatchMode(ctx, tx, actor, in, true)
}

func authorizeAdminRenewalBatchMode(ctx context.Context, tx pgx.Tx, actor int64, in AdminRenewalBatchInput, lockResources bool) (bool, error) {
	if err := lockAdminRenewalAuthority(ctx, tx, actor, in.CustomerID, "services.view", "invoices.view", adminRenewalBatchPermission(in.Action)); err != nil {
		return false, err
	}
	var active bool
	lockClause := ""
	if lockResources {
		lockClause = " FOR SHARE"
	}
	if err := tx.QueryRow(ctx, "SELECT status='active' FROM users WHERE id=$1 AND role='customer'"+lockClause, in.CustomerID).Scan(&active); err != nil {
		return false, err
	}
	serviceIDs, invoiceIDs := make([]int64, 0, len(in.Items)), []int64{}
	for _, item := range in.Items {
		serviceIDs = append(serviceIDs, item.ServiceID)
		if item.InvoiceID > 0 {
			invoiceIDs = append(invoiceIDs, item.InvoiceID)
		}
	}
	for _, resource := range []struct {
		table string
		ids   []int64
	}{{"invoices", invoiceIDs}, {"services", serviceIDs}} {
		if len(resource.ids) == 0 {
			continue
		}
		lockClause := ""
		if lockResources {
			lockClause = " FOR SHARE NOWAIT"
		}
		rows, err := tx.Query(ctx, "SELECT id,user_id FROM "+resource.table+" WHERE id=ANY($1) ORDER BY id"+lockClause, resource.ids)
		if err != nil {
			return false, cancellationServiceLockError(err)
		}
		count, valid := 0, true
		for rows.Next() {
			var id, owner int64
			if err = rows.Scan(&id, &owner); err != nil {
				rows.Close()
				return false, err
			}
			count++
			valid = valid && owner == in.CustomerID
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return false, err
		}
		if !valid || count != len(resource.ids) {
			return false, pgx.ErrNoRows
		}
	}
	return active, checkAdminRenewalCredential(ctx, tx, actor)
}

func (s *Store) AdminRenewalBatchQuote(ctx context.Context, actor int64, in AdminRenewalBatchQuoteInput) (map[string]any, error) {
	manifest := AdminRenewalBatchInput{CustomerID: in.CustomerID, Action: "create"}
	if actor < 1 || in.CustomerID < 1 || len(in.Items) < 1 || len(in.Items) > adminRenewalBatchLimit {
		return nil, renewalBatchInvalid("请提供客户和1至20项服务")
	}
	seen := map[int64]bool{}
	for _, item := range in.Items {
		if item.ServiceID < 1 || item.ExpectedVersion < 1 || seen[item.ServiceID] {
			return nil, renewalBatchInvalid("服务与版本必须有效，服务不能重复")
		}
		seen[item.ServiceID] = true
		manifest.Items = append(manifest.Items, AdminRenewalBatchItemInput{ServiceID: item.ServiceID, ExpectedVersion: item.ExpectedVersion})
	}
	if err := s.checkAdminRenewalBatch(ctx, actor, manifest); err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, adminRenewalBatchCustomerKey{}, in.CustomerID)
	items := make([]map[string]any, 0, len(in.Items))
	for _, item := range in.Items {
		quote, err := s.AdminRenewalQuote(ctx, actor, item.ServiceID, item.ExpectedVersion)
		entry := map[string]any{"service_id": item.ServiceID, "status": "quoted"}
		if err != nil {
			failure, ok := renewalBatchBusinessFailure(err)
			if !ok {
				return nil, err
			}
			entry["status"], entry["code"], entry["error"] = "failed", failure.Code, failure.Error
		} else {
			entry["quote"] = quote
		}
		items = append(items, entry)
	}
	if err := s.checkAdminRenewalBatch(ctx, actor, manifest); err != nil {
		return nil, err
	}
	return map[string]any{"customer_id": in.CustomerID, "items": items}, nil
}

func (s *Store) checkAdminRenewalBatch(ctx context.Context, actor int64, in AdminRenewalBatchInput) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = authorizeAdminRenewalBatch(ctx, tx, actor, in); err != nil {
		return err
	}
	return commitAdminRenewal(ctx, tx, actor)
}

func (s *Store) AdminCreateRenewalBatch(ctx context.Context, actor int64, in AdminRenewalBatchInput, key string) (AdminRenewalBatchResult, error) {
	var out AdminRenewalBatchResult
	key = strings.TrimSpace(key)
	if err := validateRenewalBatchInput(&in); err != nil {
		return out, err
	}
	if actor < 1 || len(key) < 8 || len(key) > 128 {
		return out, renewalBatchInvalid("请提供有效管理员及8至128字节幂等键")
	}
	manifest := adminRenewalBatchManifest{accountBusinessHash(struct {
		Actor int64
		Key   string
	}{actor, key}), in}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	active, err := authorizeAdminRenewalBatch(ctx, tx, actor, in)
	if err != nil {
		return out, err
	}
	hash := accountBusinessHash(in)
	replayed, err := ReplayOperation(ctx, tx, actor, adminRenewalBatchScope, manifest.BatchID, hash, nil)
	if err == nil || isRenewalBatchConflict(err) {
		if authErr := checkAdminRenewalCredential(ctx, tx, actor); authErr != nil {
			return out, authErr
		}
	}
	if err != nil {
		return out, err
	}
	if !replayed {
		if !active {
			return out, conflict("customer_inactive", "客户已停用，不能登记新续费批次")
		}
		if err = SaveOperation(ctx, tx, actor, adminRenewalBatchScope, manifest.BatchID, hash, manifest); err != nil {
			return out, err
		}
		if err = Audit(ctx, tx, actor, "service.renewal.batch.created", "customer", in.CustomerID, map[string]any{"batch_id": manifest.BatchID, "action": in.Action, "reason": in.Reason, "items": in.Items}); err != nil {
			return out, err
		}
	}
	if err = commitAdminRenewal(ctx, tx, actor); err != nil {
		return out, err
	}
	out, err = s.AdminRenewalBatch(ctx, actor, manifest.BatchID)
	out.Replayed = replayed
	return out, err
}

func isRenewalBatchConflict(err error) bool {
	var failure *Conflict
	return errors.As(err, &failure)
}

func loadAdminRenewalBatch(ctx context.Context, tx pgx.Tx, actor int64, id string) (adminRenewalBatchManifest, error) {
	var out adminRenewalBatchManifest
	if actor < 1 || !validRenewalBatchID(id) {
		return out, renewalBatchInvalid("续费批次编号无效")
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT result FROM mutation_operations WHERE actor_id=$1 AND scope=$2 AND idempotency_key=$3", actor, adminRenewalBatchScope, id).Scan(&raw); err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

func renewalBatchItemKey(batch string, service int64) string {
	return "batch-renewal:" + accountBusinessHash(struct {
		Batch   string
		Service int64
	}{batch, service})
}

func renewalBatchAttemptScope(batch string) string {
	return adminRenewalBatchScope + ".execute:" + batch
}
func renewalBatchOutcomeScope(batch string) string {
	return adminRenewalBatchScope + ".outcome:" + batch
}
func renewalBatchOutcomeKey(attempt string, service int64) string {
	return accountBusinessHash(struct {
		Attempt string
		Service int64
	}{attempt, service})
}

func renewalBatchChildScope(action string) string {
	if action == "pay_balance" {
		return adminRenewalPaymentScope
	}
	return adminRenewalCreateScope
}

func renewalBatchChildHash(manifest adminRenewalBatchManifest, item AdminRenewalBatchItemInput) string {
	if manifest.Action == "create" {
		return adminRenewalCreateRequestHash(manifest.CustomerID, item.ServiceID, AdminServiceRenewalInput{ExpectedVersion: item.ExpectedVersion, QuoteHash: item.QuoteHash, Reason: manifest.Reason})
	}
	return adminRenewalPaymentRequestHash(manifest.CustomerID, item.ServiceID, item.InvoiceID, AdminRenewalPaymentInput{ExpectedVersion: item.ExpectedVersion, AmountCents: *item.AmountCents, Reason: manifest.Reason})
}

func (s *Store) AdminRenewalBatch(ctx context.Context, actor int64, id string) (AdminRenewalBatchResult, error) {
	var out AdminRenewalBatchResult
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	manifest, err := loadAdminRenewalBatch(ctx, tx, actor, id)
	if err != nil {
		return out, err
	}
	if _, err = authorizeAdminRenewalBatchMode(ctx, tx, actor, manifest.AdminRenewalBatchInput, false); err != nil {
		return out, err
	}
	out = AdminRenewalBatchResult{BatchID: id, CustomerID: manifest.CustomerID, Action: manifest.Action, Reason: manifest.Reason, Items: make([]AdminRenewalBatchItem, 0, len(manifest.Items))}
	// Outcome rows are observations, never financial truth. A committed child
	// receipt always wins, including a lost COMMIT or progress acknowledgement.
	attempted, outcomes := map[int64]bool{}, map[int64]adminRenewalBatchOutcome{}
	rows, err := tx.Query(ctx, `SELECT scope,result FROM mutation_operations WHERE actor_id=$1 AND scope=ANY($2) ORDER BY created_at,idempotency_key`, actor, []string{renewalBatchAttemptScope(id), renewalBatchOutcomeScope(id)})
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var scope string
		var raw []byte
		if err = rows.Scan(&scope, &raw); err != nil {
			break
		}
		if scope == renewalBatchAttemptScope(id) {
			var attempt adminRenewalBatchAttempt
			if err = json.Unmarshal(raw, &attempt); err != nil {
				break
			}
			for _, service := range attempt.ServiceIDs {
				attempted[service] = true
				delete(outcomes, service)
			}
		} else {
			var outcome adminRenewalBatchOutcome
			if err = json.Unmarshal(raw, &outcome); err != nil {
				break
			}
			outcomes[outcome.ServiceID] = outcome
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	for _, item := range manifest.Items {
		entry := AdminRenewalBatchItem{AdminRenewalBatchItemInput: item, Status: "pending"}
		var hash string
		err = tx.QueryRow(ctx, "SELECT request_hash,result FROM mutation_operations WHERE actor_id=$1 AND scope=$2 AND idempotency_key=$3", actor, renewalBatchChildScope(manifest.Action), renewalBatchItemKey(id, item.ServiceID)).Scan(&hash, &entry.Result)
		switch {
		case err == nil:
			if hash != renewalBatchChildHash(manifest, item) {
				return out, conflict("idempotency_conflict", "单项回执与原批次不一致，请先核对原操作")
			}
			entry.Status = "succeeded"
			out.Succeeded++
		case !errors.Is(err, pgx.ErrNoRows):
			return out, err
		case outcomes[item.ServiceID].Code != "":
			entry.Status, entry.Code, entry.Error = "failed", outcomes[item.ServiceID].Code, outcomes[item.ServiceID].Error
			out.Failed++
		case attempted[item.ServiceID]:
			entry.Status = "unconfirmed"
			out.Unconfirmed++
		default:
			out.Pending++
		}
		out.Items = append(out.Items, entry)
	}
	// Progress must remain readable while an item holds wallet/invoice/service
	// locks. Recheck membership after assembling it instead of waiting on those
	// rows; action commands retain their own locked ownership checks.
	if _, err = authorizeAdminRenewalBatchMode(ctx, tx, actor, manifest.AdminRenewalBatchInput, false); err != nil {
		return out, err
	}
	return out, commitAdminRenewal(ctx, tx, actor)
}

// Each execute key is one explicit attempt. Its definitive failures are frozen;
// only a new execute key may retry them. Child success keys span all attempts.
func (s *Store) AdminExecuteRenewalBatch(ctx context.Context, actor int64, id string, in AdminRenewalBatchExecutionInput, key string) (AdminRenewalBatchResult, error) {
	var out AdminRenewalBatchResult
	key = strings.TrimSpace(key)
	if actor < 1 || !validRenewalBatchID(id) || len(key) < 8 || len(key) > 128 || len(in.ServiceIDs) < 1 || len(in.ServiceIDs) > adminRenewalBatchLimit {
		return out, renewalBatchInvalid("请提供有效批次、执行键和1至20项服务")
	}
	// Authorize before reserving the coordinator connection. It holds no row
	// locks/transaction, and try-lock refuses concurrent execution immediately.
	if _, err := s.AdminRenewalBatch(ctx, actor, id); err != nil {
		return out, err
	}
	select {
	case adminRenewalBatchSlots <- struct{}{}:
		defer func() { <-adminRenewalBatchSlots }()
	default:
		return out, conflict("renewal_batch_capacity", "当前批量执行已满，请查询进度后使用原执行键恢复")
	}
	coordinator, err := pgx.ConnectConfig(ctx, s.Pool.Config().ConnConfig.Copy())
	if err != nil {
		return out, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Closing the session releases its advisory lock even if a request was
		// canceled or the lock acknowledgement itself was lost.
		coordinator.Close(cleanup)
	}()
	lockName := fmt.Sprintf("admin-renewal-batch:%d:%s", actor, id)
	var locked bool
	err = coordinator.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended(current_database()||':'||current_schema()||':'||$1,0))", lockName).Scan(&locked)
	if err != nil {
		return out, err
	}
	if !locked {
		return out, conflict("renewal_batch_busy", "该批次正在执行，请查询进度后使用原执行键恢复")
	}
	manifest, attempt, err := s.startAdminRenewalBatchAttempt(ctx, actor, id, in, key)
	if err != nil {
		return out, err
	}
	ctx = context.WithValue(ctx, adminRenewalBatchCustomerKey{}, manifest.CustomerID)
	selected := map[int64]bool{}
	for _, service := range in.ServiceIDs {
		selected[service] = true
	}
	for _, item := range manifest.Items {
		if !selected[item.ServiceID] {
			continue
		}
		done, err := s.adminRenewalBatchItemDone(ctx, actor, manifest, item.ServiceID, attempt)
		if err != nil {
			return out, err
		}
		if done {
			continue
		}
		childKey := renewalBatchItemKey(id, item.ServiceID)
		if manifest.Action == "create" {
			_, err = s.AdminCreateRenewalInvoice(ctx, actor, item.ServiceID, AdminServiceRenewalInput{ExpectedVersion: item.ExpectedVersion, QuoteHash: item.QuoteHash, Reason: manifest.Reason}, childKey)
		} else {
			_, err = s.AdminPayRenewalInvoice(ctx, actor, item.ServiceID, item.InvoiceID, AdminRenewalPaymentInput{ExpectedVersion: item.ExpectedVersion, AmountCents: *item.AmountCents, Reason: manifest.Reason}, childKey)
		}
		if err != nil {
			failure, known := renewalBatchBusinessFailure(err)
			if !known {
				return out, err
			}
			failure.ServiceID = item.ServiceID
			if err = s.saveAdminRenewalBatchFailure(ctx, actor, manifest, attempt, failure); err != nil {
				return out, err
			}
		}
	}
	return s.AdminRenewalBatch(ctx, actor, id)
}

func (s *Store) startAdminRenewalBatchAttempt(ctx context.Context, actor int64, id string, in AdminRenewalBatchExecutionInput, key string) (adminRenewalBatchManifest, string, error) {
	var manifest adminRenewalBatchManifest
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return manifest, "", err
	}
	defer tx.Rollback(ctx)
	manifest, err = loadAdminRenewalBatch(ctx, tx, actor, id)
	if err != nil {
		return manifest, "", err
	}
	if _, err = authorizeAdminRenewalBatch(ctx, tx, actor, manifest.AdminRenewalBatchInput); err != nil {
		return manifest, "", err
	}
	selected := map[int64]bool{}
	for _, service := range in.ServiceIDs {
		if service < 1 || selected[service] {
			return manifest, "", renewalBatchInvalid("执行服务无效或重复")
		}
		selected[service] = true
	}
	canonical := adminRenewalBatchAttempt{ServiceIDs: []int64{}}
	for _, item := range manifest.Items {
		if selected[item.ServiceID] {
			canonical.ServiceIDs = append(canonical.ServiceIDs, item.ServiceID)
		}
	}
	if len(canonical.ServiceIDs) != len(selected) {
		return manifest, "", renewalBatchInvalid("执行项必须属于原批次")
	}
	attempt := accountBusinessHash(key)
	hash := accountBusinessHash(canonical)
	replayed, err := ReplayOperation(ctx, tx, actor, renewalBatchAttemptScope(id), attempt, hash, nil)
	if err == nil || isRenewalBatchConflict(err) {
		if authErr := checkAdminRenewalCredential(ctx, tx, actor); authErr != nil {
			return manifest, "", authErr
		}
	}
	if err != nil {
		return manifest, "", err
	}
	if !replayed {
		if err = SaveOperation(ctx, tx, actor, renewalBatchAttemptScope(id), attempt, hash, canonical); err != nil {
			return manifest, "", err
		}
		if err = Audit(ctx, tx, actor, "service.renewal.batch.execution_requested", "customer", manifest.CustomerID, map[string]any{"batch_id": id, "attempt_id": attempt, "service_ids": canonical.ServiceIDs, "action": manifest.Action}); err != nil {
			return manifest, "", err
		}
	}
	return manifest, attempt, commitAdminRenewal(ctx, tx, actor)
}

func (s *Store) adminRenewalBatchItemDone(ctx context.Context, actor int64, manifest adminRenewalBatchManifest, service int64, attempt string) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err = authorizeAdminRenewalBatch(ctx, tx, actor, manifest.AdminRenewalBatchInput); err != nil {
		return false, err
	}
	var done bool
	var hash string
	err = tx.QueryRow(ctx, "SELECT request_hash FROM mutation_operations WHERE actor_id=$1 AND scope=$2 AND idempotency_key=$3", actor, renewalBatchChildScope(manifest.Action), renewalBatchItemKey(manifest.BatchID, service)).Scan(&hash)
	if err == nil {
		for _, item := range manifest.Items {
			if item.ServiceID == service && hash != renewalBatchChildHash(manifest, item) {
				return false, conflict("idempotency_conflict", "单项回执与原批次不一致，请先核对原操作")
			}
		}
		done = true
	} else if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mutation_operations WHERE actor_id=$1 AND scope=$2 AND idempotency_key=$3)", actor, renewalBatchOutcomeScope(manifest.BatchID), renewalBatchOutcomeKey(attempt, service)).Scan(&done)
	}
	if err != nil {
		return false, err
	}
	return done, commitAdminRenewal(ctx, tx, actor)
}

func (s *Store) saveAdminRenewalBatchFailure(ctx context.Context, actor int64, manifest adminRenewalBatchManifest, attempt string, failure adminRenewalBatchOutcome) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = authorizeAdminRenewalBatch(ctx, tx, actor, manifest.AdminRenewalBatchInput); err != nil {
		return err
	}
	key := renewalBatchOutcomeKey(attempt, failure.ServiceID)
	hash := accountBusinessHash(struct {
		Attempt string
		Service int64
	}{attempt, failure.ServiceID})
	replayed, err := ReplayOperation(ctx, tx, actor, renewalBatchOutcomeScope(manifest.BatchID), key, hash, nil)
	if err != nil {
		return err
	}
	if !replayed {
		if err = SaveOperation(ctx, tx, actor, renewalBatchOutcomeScope(manifest.BatchID), key, hash, failure); err != nil {
			return err
		}
	}
	return commitAdminRenewal(ctx, tx, actor)
}

// Authentication/scope loss and unknown database/commit outcomes stop execution.
// They must never be persisted as definitive business failures or retried under
// a replacement child key. Only explicit, safe domain errors can continue.
func renewalBatchBusinessFailure(err error) (adminRenewalBatchOutcome, bool) {
	var failure *Conflict
	if errors.As(err, &failure) {
		if failure.Code == "unauthorized" || failure.Code == "permission_denied" {
			return adminRenewalBatchOutcome{}, false
		}
		return adminRenewalBatchOutcome{Code: failure.Code, Error: failure.Message}, true
	}
	var account *AccountBusinessError
	if errors.As(err, &account) && account.Status == 400 {
		return adminRenewalBatchOutcome{Code: account.Code, Error: account.Message}, true
	}
	return adminRenewalBatchOutcome{}, false
}
