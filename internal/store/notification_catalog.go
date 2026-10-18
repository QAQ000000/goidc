package store

import "strings"

// NotificationEventDescriptor is the shared contract for the administrator UI
// and the non-executable template vocabulary. Channels do not own event kinds.
type NotificationEventDescriptor struct {
	Kind        string                 `json:"kind"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Resource    string                 `json:"resource"`
	Variables   []NotificationVariable `json:"variables"`
	Reminder    bool                   `json:"reminder"`
}

type NotificationVariable struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

func notificationVariables(keys ...string) []NotificationVariable {
	labels := map[string]string{"invoice_no": "账单号", "amount": "金额", "order_no": "订单号", "service_id": "服务编号", "service_name": "服务名称", "product_name": "产品名称", "domain": "服务标识", "due_date": "到期日期（上海时区）", "days_left": "提前天数"}
	out := make([]NotificationVariable, 0, len(keys))
	for _, key := range keys {
		out = append(out, NotificationVariable{Key: key, Label: labels[key]})
	}
	return out
}

// NotificationEventCatalog returns fresh slices so callers cannot alter the
// validation policy used by subsequent requests.
func NotificationEventCatalog() []NotificationEventDescriptor {
	service := func() []NotificationVariable {
		return notificationVariables("service_id", "service_name", "product_name", "domain", "due_date")
	}
	return []NotificationEventDescriptor{
		{Kind: "invoice.paid", Name: "付款成功", Resource: "invoice", Description: "账单付款成功的事实；兼容原付款成功订阅。", Variables: notificationVariables("invoice_no", "amount")},
		{Kind: "order.created", Name: "下单成功", Resource: "order", Description: "订单创建成功；付款和服务开通分别通知。", Variables: notificationVariables("order_no", "amount")},
		{Kind: "service.provisioned", Name: "服务开通", Resource: "service", Description: "实际服务交付完成。", Variables: service()},
		{Kind: "service.suspended", Name: "服务暂停", Resource: "service", Description: "实际暂停完成；发送前若已恢复则抑制。", Variables: service()},
		{Kind: "service.resumed", Name: "服务恢复", Resource: "service", Description: "实际恢复完成。", Variables: service()},
		{Kind: "service.renewed", Name: "服务续费", Resource: "service", Description: "续费权益实际生效。", Variables: service()},
		{Kind: "service.terminated", Name: "服务终止", Resource: "service", Description: "实际终止完成。", Variables: service()},
		{Kind: "service.expiry_reminder", Name: "服务即将到期", Resource: "service", Description: "到期前提醒；续费延期后原提醒自动失效。", Variables: notificationVariables("service_id", "service_name", "product_name", "domain", "due_date", "days_left"), Reminder: true},
		{Kind: "service.expired", Name: "服务已到期", Resource: "service", Description: "当前服务到期；续费延期后原提醒自动失效。", Variables: service(), Reminder: true},
		{Kind: "order.payment_reminder", Name: "待支付订单", Resource: "invoice", Description: "待支付订单提醒；付款、取消或截止变更后失效。", Variables: notificationVariables("order_no", "invoice_no", "amount", "due_date"), Reminder: true},
		{Kind: "invoice.overdue", Name: "账单逾期", Resource: "invoice", Description: "未付账单催缴；付款、取消或截止变更后失效。", Variables: notificationVariables("order_no", "invoice_no", "amount", "due_date"), Reminder: true},
	}
}

func notificationEventDescriptor(kind string) (NotificationEventDescriptor, bool) {
	for _, event := range NotificationEventCatalog() {
		if event.Kind == kind {
			return event, true
		}
	}
	return NotificationEventDescriptor{}, false
}

func notificationTemplateValid(kind, value string) bool {
	event, ok := notificationEventDescriptor(kind)
	if !ok {
		return false
	}
	for _, variable := range event.Variables {
		value = strings.ReplaceAll(value, "{{"+variable.Key+"}}", "")
	}
	return !strings.Contains(value, "{{") && !strings.Contains(value, "}}")
}
