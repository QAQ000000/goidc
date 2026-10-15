package store

import (
	"errors"
	"testing"
)

// 自动续费错误文案的纯函数测试：旧系统没有结构化错误列，失败只写
// activity_log 自由文本；Go 版有 services.auto_renew_error，必须区分
// "明确跳过（已有未付续费账单）"与真实失败。

func TestAutoRenewErrorTextLabelsPendingInvoice(t *testing.T) {
	pending := conflict("renewal_invoice_pending", "该服务已有待支付的续费账单")
	if got := autoRenewErrorText(pending); got != autoRenewPendingInvoice {
		t.Fatalf("pending invoice label=%q want %q", got, autoRenewPendingInvoice)
	}
	// 包装过的错误同样要识别。
	wrapped := errors.Join(errors.New("context"), pending)
	if got := autoRenewErrorText(wrapped); got != autoRenewPendingInvoice {
		t.Fatalf("wrapped pending invoice label=%q want %q", got, autoRenewPendingInvoice)
	}
	insufficient := conflict("insufficient_balance", "账户余额不足")
	if got := autoRenewErrorText(insufficient); got != "账户余额不足" {
		t.Fatalf("real failure must keep its message, got %q", got)
	}
	if got := autoRenewErrorText(errors.New("boom")); got != "boom" {
		t.Fatalf("plain error must keep its message, got %q", got)
	}
}

func TestAutoRenewSkipReasonNeverTreatsReplayAsPaid(t *testing.T) {
	insufficient := conflict("insufficient_balance", "账户余额不足")
	cases := []struct {
		name     string
		payErr   error
		status   string
		replayed bool
		want     string
	}{
		// 重放命中未支付账单：旧系统 createRenew 的等价场景是直接跳过，
		// 绝不能被当成"生成续费账单成功"。
		{"replayed unpaid", insufficient, "unpaid", true, autoRenewPendingInvoice},
		{"first pass unpaid", insufficient, "unpaid", false, "账户余额不足"},
		{"replayed refunded", insufficient, "refunded", true, "续费账单状态为 refunded，未完成自动续费"},
		{"replayed cancelled", insufficient, "cancelled", true, "续费账单状态为 cancelled，未完成自动续费"},
	}
	for _, tc := range cases {
		got := autoRenewSkipReason(tc.payErr, tc.status, tc.replayed)
		if got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
