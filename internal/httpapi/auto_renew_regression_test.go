package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

// 自动续费回归测试（2026-10-15）。
//
// 旧系统权威语义（app/admin/command/Cron.php::invoice() +
// app/common/logic/Invoices.php::createRenew()）：
//  1. 候选查询没有 LIMIT、没有 ORDER BY，每日全量处理，结构上不存在饿死；
//  2. 服务已有未支付的续费账单时立即跳过（返回 status=500 的
//     "生成续费账单成功"，调用方只统计 status==200，因此既不重建账单、
//     也不重试扣款、更不谎报成功）；
//  3. 余额不足直接放弃，host 表没有重试/退避字段，以"只做一次"代替重试。
//
// Go 版是 worker 轮询模型，保留 LIMIT，因此必须靠"已有未付续费账单即排除"
// 防止长期失败的服务永久占满窗口；同时 auto_renew_error 是旧系统缺失的
// 结构化运维字段，失败必须写入，且账单重放绝不能被当成续费成功。

// autoRenewBuy 为一个全新客户下单、支付并完成模拟开通，
// 返回客户 ID、服务 ID 和该客户的会话 Cookie。
func autoRenewBuy(t *testing.T, db *store.Store, h http.Handler, admin, name string, pid, topup int64) (int64, int64, string) {
	t.Helper()
	uid, cookie := user(t, db, h, name, "customer")
	if topup > 0 {
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), map[string]any{"amount_cents": topup}, admin, "autorenew-topup-"+name), 201)
	}
	version, itemID := add(t, h, cookie, pid, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, cookie, "autorenew-order-"+name)
	expect(t, order, 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(data(t, order)["invoice_id"].(float64))), map[string]any{}, cookie, "autorenew-initial-pay-"+name), 201)
	job, err := db.ClaimProvisionJob(context.Background())
	if err != nil || job == nil {
		t.Fatalf("autorenew fixture %s: claim provision job: %v", name, err)
	}
	if err = db.ProcessProvisionJob(context.Background(), *job); err != nil {
		t.Fatal(err)
	}
	var sid int64
	if err = db.Pool.QueryRow(context.Background(), "SELECT id FROM services WHERE user_id=$1", uid).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	return uid, sid, cookie
}

// autoRenewArm 通过正式接口开启自动续费，再把 next_due_at 调整到相对当前
// 时刻的指定分钟数（候选窗口是 now()+interval '24 hours'）。
func autoRenewArm(t *testing.T, db *store.Store, h http.Handler, cookie string, serviceID int64, minutesAhead int) {
	t.Helper()
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/services/%d/auto-renew", serviceID), map[string]bool{"enabled": true}, cookie, ""), 200)
	if _, err := db.Pool.Exec(context.Background(), "UPDATE services SET next_due_at=now()+make_interval(mins=>$2) WHERE id=$1", serviceID, minutesAhead); err != nil {
		t.Fatal(err)
	}
}

// autoRenewInvoiceStats 统计某服务的续费账单（按账单实际状态区分）。
func autoRenewInvoiceStats(t *testing.T, db *store.Store, serviceID int64) (paid, unpaid, total int) {
	t.Helper()
	if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE i.status='paid'),count(*) FILTER (WHERE i.status='unpaid'),count(*)
FROM invoices i JOIN invoice_items ii ON ii.invoice_id=i.id
WHERE ii.service_id=$1 AND ii.item_type='renew'`, serviceID).Scan(&paid, &unpaid, &total); err != nil {
		t.Fatal(err)
	}
	return paid, unpaid, total
}

// autoRenewState 读取服务的到期时间、结构化错误和自动续费开关。
func autoRenewState(t *testing.T, db *store.Store, serviceID int64) (time.Time, string, bool) {
	t.Helper()
	var due time.Time
	var errText string
	var enabled bool
	if err := db.Pool.QueryRow(context.Background(), "SELECT next_due_at,auto_renew_error,auto_renew FROM services WHERE id=$1", serviceID).Scan(&due, &errText, &enabled); err != nil {
		t.Fatal(err)
	}
	return due, errText, enabled
}

// TestAutoRenewFailingServicesDoNotStarveTheWindow 覆盖缺陷 1：余额不足的服务
// 反复失败、next_due_at 不变，修复前在 LIMIT 窗口内永久占位，排在后面的
// 可续费服务永远轮不到。修复后它们因"已有未付续费账单"被排除出候选。
func TestAutoRenewFailingServicesDoNotStarveTheWindow(t *testing.T) {
	ctx := context.Background()
	db, h := testServer(t)
	_, admin := user(t, db, h, "autorenew-starve-admin", "super_admin")
	pid := product(t, db, 8)

	const blockedCount = 3
	blocked := make([]int64, 0, blockedCount)
	for i := 0; i < blockedCount; i++ {
		uid, sid, cookie := autoRenewBuy(t, db, h, admin, fmt.Sprintf("autorenew-starve-%d", i), pid, 5000)
		// 付完首单后清零余额：续费必然因余额不足失败，且不得被当成成功。
		if _, err := db.Pool.Exec(ctx, "UPDATE users SET balance_cents=0 WHERE id=$1", uid); err != nil {
			t.Fatal(err)
		}
		// 到期时间更早，修复前必然排在健康服务前面占满窗口。
		autoRenewArm(t, db, h, cookie, sid, 30+i)
		blocked = append(blocked, sid)
	}
	healthUID, healthySID, healthyCookie := autoRenewBuy(t, db, h, admin, "autorenew-starve-healthy", pid, 5000)
	autoRenewArm(t, db, h, healthyCookie, healthySID, 120)
	healthyDueBefore, _, _ := autoRenewState(t, db, healthySID)

	// limit 恰好等于受阻服务数量：修复前它们每一轮都占满整个窗口。
	const limit = blockedCount
	for round := 1; round <= 3; round++ {
		processed, err := db.ProcessAutoRenewals(ctx, limit)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("round %d: limit=%d processed=%d", round, limit, processed)
	}
	// 首要断言：修复前受阻服务永久占位，健康服务三轮内一次都不会被处理。
	paid, unpaid, total := autoRenewInvoiceStats(t, db, healthySID)
	healthyDueAfter, errText, _ := autoRenewState(t, db, healthySID)
	t.Logf("healthy service %d: invoices(paid=%d unpaid=%d total=%d) due %s -> %s auto_renew_error=%q balance=%d",
		healthySID, paid, unpaid, total, healthyDueBefore, healthyDueAfter, errText, autoRenewBalance(t, db, healthUID))
	if paid != 1 || unpaid != 0 || total != 1 {
		t.Fatalf("余额充足的服务被饿死: paid=%d unpaid=%d total=%d", paid, unpaid, total)
	}
	if !healthyDueAfter.After(healthyDueBefore) {
		t.Fatalf("健康服务到期时间未推进: %s -> %s", healthyDueBefore, healthyDueAfter)
	}
	if errText != "" {
		t.Fatalf("续费成功后不应残留错误: %q", errText)
	}
	for _, sid := range blocked {
		paid, unpaid, total := autoRenewInvoiceStats(t, db, sid)
		_, errText, _ := autoRenewState(t, db, sid)
		t.Logf("blocked service %d: invoices(paid=%d unpaid=%d total=%d) auto_renew_error=%q", sid, paid, unpaid, total, errText)
		if total != 1 || unpaid != 1 || paid != 0 {
			t.Fatalf("受阻服务 %d 应恰好留下一张未支付续费账单: paid=%d unpaid=%d total=%d", sid, paid, unpaid, total)
		}
		if errText == "" {
			t.Fatalf("受阻服务 %d 的失败必须写入 auto_renew_error", sid)
		}
	}
}

// TestAutoRenewUnpaidInvoiceReplayIsNotSuccess 覆盖缺陷 2：余额不足时账单已
// 创建但未支付，下一轮重放同一幂等键时修复前 `invoice.Replayed` 被当成成功，
// processed 虚增且 auto_renew_error 被误清空。
func TestAutoRenewUnpaidInvoiceReplayIsNotSuccess(t *testing.T) {
	ctx := context.Background()
	db, h := testServer(t)
	_, admin := user(t, db, h, "autorenew-replay-admin", "super_admin")
	pid := product(t, db, 2)
	uid, sid, cookie := autoRenewBuy(t, db, h, admin, "autorenew-replay-owner", pid, 5000)
	if _, err := db.Pool.Exec(ctx, "UPDATE users SET balance_cents=0 WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	autoRenewArm(t, db, h, cookie, sid, 30)

	first, err := db.ProcessAutoRenewals(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	paid, unpaid, total := autoRenewInvoiceStats(t, db, sid)
	_, firstErr, _ := autoRenewState(t, db, sid)
	t.Logf("first pass: processed=%d invoices(paid=%d unpaid=%d total=%d) auto_renew_error=%q", first, paid, unpaid, total, firstErr)
	if first != 0 {
		t.Fatalf("余额不足的服务不得计入成功: processed=%d", first)
	}
	if paid != 0 || unpaid != 1 || total != 1 {
		t.Fatalf("首轮应留下一张未支付续费账单: paid=%d unpaid=%d total=%d", paid, unpaid, total)
	}
	firstInvoice := 0
	if err = db.Pool.QueryRow(ctx, "SELECT coalesce(min(i.id),0) FROM invoices i JOIN invoice_items ii ON ii.invoice_id=i.id WHERE ii.service_id=$1 AND ii.item_type='renew'", sid).Scan(&firstInvoice); err != nil {
		t.Fatal(err)
	}
	if firstErr == "" {
		t.Fatal("首轮失败必须写入 auto_renew_error")
	}

	// 第二轮重放同一未支付账单：修复前 processed=1 且错误被清空。
	second, err := db.ProcessAutoRenewals(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	paid, unpaid, total = autoRenewInvoiceStats(t, db, sid)
	_, secondErr, _ := autoRenewState(t, db, sid)
	var secondInvoice, secondStatus string
	if err = db.Pool.QueryRow(ctx, "SELECT id::text,status FROM invoices WHERE id=$1", firstInvoice).Scan(&secondInvoice, &secondStatus); err != nil {
		t.Fatal(err)
	}
	t.Logf("second pass: processed=%d invoices(paid=%d unpaid=%d total=%d) invoice %s status=%s auto_renew_error=%q balance=%d",
		second, paid, unpaid, total, secondInvoice, secondStatus, secondErr, autoRenewBalance(t, db, uid))
	if second != 0 {
		t.Fatalf("重放未支付账单被误报为续费成功: processed=%d", second)
	}
	if paid != 0 || unpaid != 1 || total != 1 || secondStatus != "unpaid" {
		t.Fatalf("账单状态被篡改: paid=%d unpaid=%d total=%d status=%s", paid, unpaid, total, secondStatus)
	}
	if secondErr == "" {
		t.Fatal("重放未支付账单后 auto_renew_error 被误清空")
	}
	// 修复后候选查询直接排除"已有未付续费账单"的服务（旧系统同一语义），
	// 因此第二轮连账单都不会重建；错误必须是首轮失败的真实原因，而不是
	// 被 Replayed 掩盖成成功。
	if secondErr != firstErr {
		t.Fatalf("第二轮错误文案被覆盖: %q -> %q", firstErr, secondErr)
	}
	if balance := autoRenewBalance(t, db, uid); balance != 0 {
		t.Fatalf("续费失败不应改变余额: %d", balance)
	}
}

// TestAutoRenewReplayOfNonPaidInvoiceIsNotCounted 覆盖缺陷 2 的另一半：账单
// 幂等键命中重放、但账单实际并非已支付（这里把首轮已支付续费账单改为
// cancelled 后重放）时，修复前 `payErr == nil || invoice.Replayed` 因重放为真
// 而误报成功并清空错误；修复后以 invoices.status 为准，必须不计入成功。
func TestAutoRenewReplayOfNonPaidInvoiceIsNotCounted(t *testing.T) {
	ctx := context.Background()
	db, h := testServer(t)
	_, admin := user(t, db, h, "autorenew-cancelled-admin", "super_admin")
	pid := product(t, db, 2)
	uid, sid, cookie := autoRenewBuy(t, db, h, admin, "autorenew-cancelled-owner", pid, 5000)
	autoRenewArm(t, db, h, cookie, sid, 30)
	// 记录服务当前到期时间：重放要求幂等键一致，即候选 due 必须与首轮相同。
	dueForKey, _, _ := autoRenewState(t, db, sid)

	processed, err := db.ProcessAutoRenewals(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	paid, unpaid, total := autoRenewInvoiceStats(t, db, sid)
	t.Logf("baseline pass: processed=%d invoices(paid=%d unpaid=%d total=%d) dueForKey=%s", processed, paid, unpaid, total, dueForKey)
	if processed != 1 || paid != 1 || total != 1 {
		t.Fatalf("前置条件不成立: processed=%d paid=%d total=%d", processed, paid, total)
	}
	var invoiceID int64
	if err = db.Pool.QueryRow(ctx, `SELECT i.id FROM invoices i JOIN invoice_items ii ON ii.invoice_id=i.id
WHERE ii.service_id=$1 AND ii.item_type='renew'`, sid).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	// 把该续费账单改成非已支付状态，并恢复原始到期时间，使下一轮幂等键命中
	// 同一张账单（Replayed=true）而账单并非已支付。
	if _, err = db.Pool.Exec(ctx, "UPDATE invoices SET status='cancelled',paid_at=NULL WHERE id=$1", invoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE services SET next_due_at=$2 WHERE id=$1", sid, dueForKey); err != nil {
		t.Fatal(err)
	}
	balanceBefore := autoRenewBalance(t, db, uid)

	replay, err := db.ProcessAutoRenewals(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	paid, unpaid, total = autoRenewInvoiceStats(t, db, sid)
	_, errText, _ := autoRenewState(t, db, sid)
	var status string
	if err = db.Pool.QueryRow(ctx, "SELECT status FROM invoices WHERE id=$1", invoiceID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	t.Logf("replay pass: processed=%d invoice=%d status=%s invoices(paid=%d unpaid=%d total=%d) auto_renew_error=%q balance=%d->%d",
		replay, invoiceID, status, paid, unpaid, total, errText, balanceBefore, autoRenewBalance(t, db, uid))
	if replay != 0 {
		t.Fatalf("重放非已支付账单被误报为续费成功: processed=%d", replay)
	}
	if status != "cancelled" || paid != 0 || total != 1 {
		t.Fatalf("账单被篡改: status=%s paid=%d total=%d", status, paid, total)
	}
	if errText == "" {
		t.Fatal("重放非已支付账单必须写入 auto_renew_error")
	}
	if balance := autoRenewBalance(t, db, uid); balance != balanceBefore {
		t.Fatalf("重放非已支付账单不应扣款: %d -> %d", balanceBefore, balance)
	}
}

// TestAutoRenewPaysAndIsIdempotentAcrossPasses 正常路径不回归：余额充足时
// 续费成功、next_due_at 推进，重复调用不再产生候选也不再重复计数。
func TestAutoRenewPaysAndIsIdempotentAcrossPasses(t *testing.T) {
	ctx := context.Background()
	db, h := testServer(t)
	_, admin := user(t, db, h, "autorenew-ok-admin", "super_admin")
	pid := product(t, db, 2)
	uid, sid, cookie := autoRenewBuy(t, db, h, admin, "autorenew-ok-owner", pid, 5000)
	autoRenewArm(t, db, h, cookie, sid, 30)
	dueBefore, _, _ := autoRenewState(t, db, sid)
	balanceBefore := autoRenewBalance(t, db, uid)

	processed, err := db.ProcessAutoRenewals(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	paid, unpaid, total := autoRenewInvoiceStats(t, db, sid)
	dueAfter, errText, _ := autoRenewState(t, db, sid)
	balanceAfter := autoRenewBalance(t, db, uid)
	var amount int64
	if err = db.Pool.QueryRow(ctx, `SELECT i.amount_cents FROM invoices i JOIN invoice_items ii ON ii.invoice_id=i.id
WHERE ii.service_id=$1 AND ii.item_type='renew' AND i.status='paid'`, sid).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	t.Logf("first pass: processed=%d invoices(paid=%d unpaid=%d total=%d) amount=%d due %s -> %s balance %d -> %d error=%q",
		processed, paid, unpaid, total, amount, dueBefore, dueAfter, balanceBefore, balanceAfter, errText)
	if processed != 1 {
		t.Fatalf("余额充足的服务应续费成功: processed=%d", processed)
	}
	if paid != 1 || unpaid != 0 || total != 1 {
		t.Fatalf("续费账单应已支付且唯一: paid=%d unpaid=%d total=%d", paid, unpaid, total)
	}
	if !dueAfter.After(dueBefore) {
		t.Fatalf("续费后 next_due_at 未推进: %s -> %s", dueBefore, dueAfter)
	}
	if balanceBefore-balanceAfter != amount {
		t.Fatalf("扣款金额与账单不符: %d -> %d amount=%d", balanceBefore, balanceAfter, amount)
	}
	if errText != "" {
		t.Fatalf("成功后不应残留错误: %q", errText)
	}

	// 到期时间已推进出 24 小时窗口：重复调用不得重复计数或重复扣款。
	repeat, err := db.ProcessAutoRenewals(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	repeatPaid, repeatUnpaid, repeatTotal := autoRenewInvoiceStats(t, db, sid)
	repeatDue, repeatErr, _ := autoRenewState(t, db, sid)
	var payments int
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM balance_ledger WHERE user_id=$1 AND entry_type='payment' AND idempotency_key LIKE 'auto-pay-auto-renew-%'`, uid).Scan(&payments); err != nil {
		t.Fatal(err)
	}
	t.Logf("repeat pass: processed=%d invoices(paid=%d unpaid=%d total=%d) due=%s balance=%d payments=%d error=%q",
		repeat, repeatPaid, repeatUnpaid, repeatTotal, repeatDue, autoRenewBalance(t, db, uid), payments, repeatErr)
	if repeat != 0 || repeatPaid != 1 || repeatUnpaid != 0 || repeatTotal != 1 {
		t.Fatalf("重复调用不幂等: processed=%d paid=%d unpaid=%d total=%d", repeat, repeatPaid, repeatUnpaid, repeatTotal)
	}
	if !repeatDue.Equal(dueAfter) {
		t.Fatalf("重复调用改变了到期时间: %s -> %s", dueAfter, repeatDue)
	}
	if balance := autoRenewBalance(t, db, uid); balance != balanceAfter || payments != 1 {
		t.Fatalf("重复调用造成重复扣款: balance=%d payments=%d", balance, payments)
	}
}

// TestAutoRenewSkipsServiceWithPendingRenewalInvoice 覆盖要求 1：已有未付
// 续费账单的服务不再进入候选（旧系统 createRenew 的短路语义），因此也不会
// 占用 LIMIT 名额饿死其他服务；账单保持原状，且对运维仍可见。
func TestAutoRenewSkipsServiceWithPendingRenewalInvoice(t *testing.T) {
	ctx := context.Background()
	db, h := testServer(t)
	_, admin := user(t, db, h, "autorenew-pending-admin", "super_admin")
	pid := product(t, db, 4)

	// A：先通过人工续费接口生成一张未支付续费账单，再开启自动续费。
	uidA, sidA, cookieA := autoRenewBuy(t, db, h, admin, "autorenew-pending-manual", pid, 5000)
	manualRenew := request(h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", sidA), map[string]any{}, cookieA, "autorenew-pending-manual-renew")
	expect(t, manualRenew, 201)
	pendingInvoice := int64(data(t, manualRenew)["invoice_id"].(float64))
	autoRenewArm(t, db, h, cookieA, sidA, 30)
	dueA, _, _ := autoRenewState(t, db, sidA)
	balanceA := autoRenewBalance(t, db, uidA)

	// B：余额充足的健康服务，到期更晚；limit=1 时修复前 A 会占满唯一名额。
	_, sidB, cookieB := autoRenewBuy(t, db, h, admin, "autorenew-pending-healthy", pid, 5000)
	autoRenewArm(t, db, h, cookieB, sidB, 120)

	processed, err := db.ProcessAutoRenewals(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	paidA, unpaidA, totalA := autoRenewInvoiceStats(t, db, sidA)
	dueAAfter, errA, _ := autoRenewState(t, db, sidA)
	paidB, unpaidB, totalB := autoRenewInvoiceStats(t, db, sidB)
	dueB, errB, _ := autoRenewState(t, db, sidB)
	t.Logf("limit=1 pass: processed=%d | A(sid=%d pending_invoice=%d) invoices(paid=%d unpaid=%d total=%d) due=%s->%s error=%q balance=%d | B(sid=%d) invoices(paid=%d unpaid=%d total=%d) due=%s error=%q",
		processed, sidA, pendingInvoice, paidA, unpaidA, totalA, dueA, dueAAfter, errA, autoRenewBalance(t, db, uidA), sidB, paidB, unpaidB, totalB, dueB, errB)
	if processed != 1 {
		t.Fatalf("已有未付续费账单的服务应被排除出候选: processed=%d", processed)
	}
	if paidA != 0 || unpaidA != 1 || totalA != 1 || !dueAAfter.Equal(dueA) {
		t.Fatalf("A 的既有账单被重建或扣款: paid=%d unpaid=%d total=%d due=%s->%s", paidA, unpaidA, totalA, dueA, dueAAfter)
	}
	if after := autoRenewBalance(t, db, uidA); after != balanceA {
		t.Fatalf("A 不应被扣款: %d -> %d", balanceA, after)
	}
	if errA != "" {
		t.Fatalf("已有未付账单属于明确跳过而非失败，不应写 auto_renew_error: %q", errA)
	}
	if paidB != 1 || unpaidB != 0 || totalB != 1 {
		t.Fatalf("健康服务未被处理: paid=%d unpaid=%d total=%d", paidB, unpaidB, totalB)
	}
	// 跳过的服务对运维仍可见：未付续费账单保留在续费订单列表中。
	listed := request(h, "GET", "/api/v1/admin/renewal-orders?status=pending&limit=25&offset=0", nil, admin, "")
	expect(t, listed, 200)
	entries, ok := data(t, listed)["items"].([]any)
	if !ok {
		t.Fatalf("续费订单列表返回结构异常: %s", listed.Body.String())
	}
	found := false
	for _, entry := range entries {
		item := entry.(map[string]any)
		if int64(item["invoice_id"].(float64)) == pendingInvoice {
			found = true
			t.Logf("pending renewal order visible: invoice=%v status=%v service=%v", item["invoice_id"], item["status"], item["service_id"])
		}
	}
	if !found {
		t.Fatalf("未付续费账单 %d 应保留在续费订单列表中", pendingInvoice)
	}
}

func autoRenewBalance(t *testing.T, db *store.Store, uid int64) int64 {
	t.Helper()
	var balance int64
	if err := db.Pool.QueryRow(context.Background(), "SELECT balance_cents FROM users WHERE id=$1", uid).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	return balance
}

// TestAutoRenewCreateFailureWritesStructuredError 覆盖要求 3：CreateAutoRenewalInvoice
// 本身失败（这里用"存在未完成配置变更"构造 requireNoConfigurationChange 拒绝）
// 时，修复前是裸 continue，运维看不到任何原因；修复后必须写入 auto_renew_error，
// 且不得计入 processed、不得扣款。
func TestAutoRenewCreateFailureWritesStructuredError(t *testing.T) {
	ctx := context.Background()
	db, h := testServer(t)
	_, admin := user(t, db, h, "autorenew-create-fail-admin", "super_admin")
	pid := product(t, db, 2)
	uid, sid, cookie := autoRenewBuy(t, db, h, admin, "autorenew-create-fail-owner", pid, 5000)
	autoRenewArm(t, db, h, cookie, sid, 30)
	dueBefore, _, _ := autoRenewState(t, db, sid)
	balanceBefore := autoRenewBalance(t, db, uid)

	// 伪造一条 pending 配置变更：CreateRenewalInvoice 会以
	// configuration_change_pending 拒绝，从而走到 createErr 分支。
	if _, err := db.Pool.Exec(ctx, `INSERT INTO service_configuration_changes(
service_id,user_id,product_revision,service_version,billing_cycle,due_at,old_recurring_cents,new_recurring_cents,
amount_cents,credit_cents,old_snapshot,new_snapshot,option_ids,option_quantities,status,new_billing_cycle)
VALUES($1,$2,1,1,'monthly',now(),1234,1334,100,0,'[]','[]','[]','{}','unpaid','monthly')`, sid, uid); err != nil {
		t.Fatal(err)
	}

	processed, err := db.ProcessAutoRenewals(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	paid, unpaid, total := autoRenewInvoiceStats(t, db, sid)
	dueAfter, errText, _ := autoRenewState(t, db, sid)
	t.Logf("create failure pass: processed=%d invoices(paid=%d unpaid=%d total=%d) due=%s->%s auto_renew_error=%q balance=%d->%d",
		processed, paid, unpaid, total, dueBefore, dueAfter, errText, balanceBefore, autoRenewBalance(t, db, uid))
	if processed != 0 {
		t.Fatalf("建单失败不得计入成功: processed=%d", processed)
	}
	if total != 0 {
		t.Fatalf("建单失败不应留下续费账单: total=%d", total)
	}
	if errText == "" {
		t.Fatal("建单失败必须写入 auto_renew_error（修复前为裸 continue，运维不可见）")
	}
	if !dueAfter.Equal(dueBefore) {
		t.Fatalf("建单失败不应改变到期时间: %s -> %s", dueBefore, dueAfter)
	}
	if balance := autoRenewBalance(t, db, uid); balance != balanceBefore {
		t.Fatalf("建单失败不应扣款: %d -> %d", balanceBefore, balance)
	}
}

// TestAutoRenewCandidateFilterDoesNotChangeManualRenewalPath 覆盖要求 1 的边界：
// 排除谓词只加在 ProcessAutoRenewals 候选查询，人工续费路径仍由
// CreateRenewalInvoice 内部的 pending 检查给出 409 renewal_invoice_pending，
// 既不新建账单也不扣款。
func TestAutoRenewCandidateFilterDoesNotChangeManualRenewalPath(t *testing.T) {
	ctx := context.Background()
	db, h := testServer(t)
	_, admin := user(t, db, h, "autorenew-manual-path-admin", "super_admin")
	pid := product(t, db, 2)
	uid, sid, cookie := autoRenewBuy(t, db, h, admin, "autorenew-manual-path-owner", pid, 5000)
	autoRenewArm(t, db, h, cookie, sid, 30)

	// 自动续费成功建单后把账单恢复为未支付，模拟客户手动续费前的待支付状态。
	dueForKey, _, _ := autoRenewState(t, db, sid)
	if _, err := db.ProcessAutoRenewals(ctx, 20); err != nil {
		t.Fatal(err)
	}
	var invoiceID int64
	if err := db.Pool.QueryRow(ctx, `SELECT i.id FROM invoices i JOIN invoice_items ii ON ii.invoice_id=i.id
WHERE ii.service_id=$1 AND ii.item_type='renew'`, sid).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE invoices SET status='unpaid',paid_at=NULL WHERE id=$1", invoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE services SET next_due_at=$2 WHERE id=$1", sid, dueForKey); err != nil {
		t.Fatal(err)
	}
	before := autoRenewBalance(t, db, uid)
	manual := request(h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", sid), map[string]any{}, cookie, "autorenew-manual-path-renew")
	expect(t, manual, 409)
	manualStore, createErr := db.CreateRenewalInvoice(ctx, uid, sid, "autorenew-manual-path-key-0001")
	t.Logf("manual renewal with pending invoice: http=%d body=%s store_err=%v store_invoice=%d balance=%d->%d",
		manual.Code, manual.Body.String(), createErr, manualStore.InvoiceID, before, autoRenewBalance(t, db, uid))
	if createErr == nil {
		t.Fatal("已有未付续费账单时人工续费应被拒绝")
	}
	paid, unpaid, total := autoRenewInvoiceStats(t, db, sid)
	if paid != 0 || unpaid != 1 || total != 1 {
		t.Fatalf("拒绝建单不应产生新账单: paid=%d unpaid=%d total=%d", paid, unpaid, total)
	}
	if balance := autoRenewBalance(t, db, uid); balance != before {
		t.Fatalf("拒绝建单不应扣款: %d -> %d", before, balance)
	}
}
