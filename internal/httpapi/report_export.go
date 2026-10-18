package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/codex/zjmfgo/internal/csvexport"
)

type reportColumn struct {
	key, label string
}

type reportCSVSchema struct {
	rows    string
	columns []reportColumn
}

var reportCSVSchemas = map[string]reportCSVSchema{
	"income":         {"months", []reportColumn{{"month", "月份"}, {"income_cents", "收入（分）"}, {"invoice_count", "账单数"}}},
	"daily":          {"days", []reportColumn{{"date", "日期"}, {"new_clients", "新客户"}, {"new_orders", "新订单"}, {"paid_orders", "已支付订单"}, {"new_tickets", "新工单"}, {"ticket_replies", "工单回复"}, {"cancellation_requests", "取消申请"}}},
	"ranking":        {"", []reportColumn{{"user_id", "客户ID"}, {"display_name", "客户"}, {"income_cents", "收入（分）"}, {"expense_cents", "支出（分）"}, {"net_cents", "净额（分）"}}},
	"products":       {"products", []reportColumn{{"product_id", "产品ID"}, {"name", "产品"}, {"new_order_amount_cents", "新购收入（分）"}, {"new_order_count", "新购订单数"}, {"renewal_amount_cents", "续费收入（分）"}, {"renewal_order_count", "续费账单数"}, {"total_amount_cents", "合计（分）"}}},
	"sales":          {"items", []reportColumn{{"sales_owner_id", "销售ID"}, {"sales_owner_name", "销售"}, {"order_count", "订单数"}, {"income_cents", "收入（分）"}}},
	"tickets":        {"items", []reportColumn{{"date", "日期"}, {"ticket_count", "工单创建量"}, {"open_count", "当前待处理量"}}},
	"product_groups": {"products", []reportColumn{{"category_id", "分类ID"}, {"category_name", "分类"}, {"product_id", "产品ID"}, {"name", "产品"}, {"new_order_amount_cents", "新购收入（分）"}, {"new_order_count", "新购订单数"}, {"new_quantity", "新购数量"}, {"renewal_amount_cents", "续费收入（分）"}, {"renewal_order_count", "续费账单数"}, {"renewal_quantity", "续费数量"}, {"total_amount_cents", "合计（分）"}}},
	"annual-cash":    {"months", []reportColumn{{"month", "月份"}, {"received_cents", "现金收入（分）"}, {"refund_cents", "现金退款（分）"}, {"manual_expense_cents", "人工费用（分）"}, {"outgoing_cents", "现金支出（分）"}, {"net_cents", "现金净额（分）"}, {"business_consumption_cents", "业务消费（分）"}, {"unverified_anomaly_cents", "未核验迟到收款（分）"}, {"simulated_received_cents", "模拟收款（分）"}, {"simulated_refund_cents", "模拟退款（分）"}}},
}

// Exports use the same authorized result as the JSON response, including its
// period, resource scope, ordering and limits. Amounts remain integer cents.
func writeReport(w http.ResponseWriter, r *http.Request, name string, data any) {
	format := r.URL.Query().Get("format")
	if format == "" || format == "json" {
		write(w, http.StatusOK, data)
		return
	}
	if format != "csv" {
		writeError(w, http.StatusBadRequest, "invalid_report_format", "报表格式只支持 JSON 或 CSV")
		return
	}
	content, err := encodeReportCSV(name, data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "report_export_failed", "报表导出失败，请稍后重试")
		return
	}
	writeCSVDownload(w, "report-"+name+".csv", content)
}

// Only server-owned filenames reach this helper, after encoding has succeeded.
func writeCSVDownload(w http.ResponseWriter, filename string, content []byte) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func encodeReportCSV(name string, data any) ([]byte, error) {
	schema, ok := reportCSVSchemas[name]
	if !ok {
		return nil, fmt.Errorf("unknown report %q", name)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err = decoder.Decode(&value); err != nil {
		return nil, err
	}
	rowsValue := value
	if schema.rows != "" {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid report object")
		}
		rowsValue = object[schema.rows]
	}
	rows, ok := rowsValue.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid report rows")
	}
	if name == "income" || name == "annual-cash" {
		if total, ok := value.(map[string]any)["total"].(map[string]any); ok {
			total["month"] = "合计"
			rows = append(rows, total)
		}
	}
	headers := make([]string, len(schema.columns))
	for index, column := range schema.columns {
		headers[index] = column.label
	}
	records := make([][]any, 0, len(rows))
	for _, row := range rows {
		item, ok := row.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid report row")
		}
		cells := make([]any, len(schema.columns))
		for index, column := range schema.columns {
			cell := item[column.key]
			if cell == nil && name == "daily" && (column.key == "new_tickets" || column.key == "ticket_replies") {
				cells[index] = "无权限"
			} else if cell == nil && name == "annual-cash" && (column.key == "manual_expense_cents" || column.key == "outgoing_cents" || column.key == "net_cents") {
				cells[index] = "无权限"
			} else {
				cells[index] = cell
			}
		}
		records = append(records, cells)
	}
	return csvexport.Encode(headers, records)
}
