package httpapi

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

type productReportDetail struct {
	productIncome
	CategoryID   int64  `json:"category_id"`
	CategoryName string `json:"category_name"`
}

type productReportGroup struct {
	CategoryID      int64                 `json:"category_id"`
	CategoryName    string                `json:"category_name"`
	Products        []productReportDetail `json:"products"`
	NewAmount       int64                 `json:"new_order_amount_cents"`
	RenewalAmount   int64                 `json:"renewal_amount_cents"`
	TotalAmount     int64                 `json:"total_amount_cents"`
	NewQuantity     int64                 `json:"new_quantity"`
	RenewalQuantity int64                 `json:"renewal_quantity"`
}

func (s *Server) registerProductReportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/reports/product-income/groups", s.auth(s.productIncomeGroups, true))
}

// Category/product filters apply after complete order-line discount allocation.
// Totals and export cover all matching groups; pagination only slices the UI.
func (s *Server) productIncomeGroups(w http.ResponseWriter, r *http.Request) {
	period, err := parseReportPeriod(r, true)
	if s.reportInputFailure(w, r, err) {
		return
	}
	category, ok := categoryFilter(w, r)
	if !ok {
		return
	}
	var productID int64
	if raw := r.URL.Query().Get("product_id"); raw != "" {
		productID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || productID < 1 {
			writeError(w, 400, "invalid_product", "产品筛选无效")
			return
		}
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) > 120 {
		writeError(w, 400, "invalid_report_filter", "产品搜索条件过长")
		return
	}
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	items, err := productIncomeRowsFrom(r.Context(), tx, period, identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ProductID)
	}
	rows, err := tx.Query(r.Context(), `SELECT p.id,coalesce(c.id,0) category_id,coalesce(c.name,'未分类') category_name,coalesce(c.sort_order,0) sort_order FROM products p LEFT JOIN product_categories c ON c.id=p.category_id WHERE p.id=ANY($1::bigint[])`, ids)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	type categoryInfo struct {
		ProductID  int64
		CategoryID int64
		Name       string
		Sort       int
	}
	metadata, err := pgx.CollectRows(rows, pgx.RowToStructByPos[categoryInfo])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	byProduct := map[int64]categoryInfo{}
	for _, info := range metadata {
		byProduct[info.ProductID] = info
	}
	options := make([]map[string]any, 0, len(items))
	categories := map[int64]categoryInfo{}
	groups := map[int64]*productReportGroup{}
	filtered := make([]productReportDetail, 0, len(items))
	summary := productIncome{}
	for _, item := range items {
		info := byProduct[item.ProductID]
		options = append(options, map[string]any{"id": item.ProductID, "name": item.Name, "category_id": info.CategoryID})
		categories[info.CategoryID] = info
		if category != nil && info.CategoryID != *category || productID > 0 && item.ProductID != productID || q != "" && !strings.Contains(strings.ToLower(item.Name), strings.ToLower(q)) {
			continue
		}
		detail := productReportDetail{item, info.CategoryID, info.Name}
		filtered = append(filtered, detail)
		group := groups[info.CategoryID]
		if group == nil {
			group = &productReportGroup{CategoryID: info.CategoryID, CategoryName: info.Name, Products: []productReportDetail{}}
			groups[info.CategoryID] = group
		}
		group.Products = append(group.Products, detail)
		group.NewAmount += item.NewOrderAmountCents
		group.RenewalAmount += item.RenewalAmountCents
		group.TotalAmount += item.TotalAmountCents
		group.NewQuantity += item.NewQuantity
		group.RenewalQuantity += item.RenewalQuantity
		summary.NewOrderAmountCents += item.NewOrderAmountCents
		summary.NewOrderCount += item.NewOrderCount
		summary.RenewalAmountCents += item.RenewalAmountCents
		summary.RenewalOrderCount += item.RenewalOrderCount
		summary.TotalAmountCents += item.TotalAmountCents
		summary.NewQuantity += item.NewQuantity
		summary.RenewalQuantity += item.RenewalQuantity
	}
	categoryIDs := make([]int64, 0, len(categories))
	for id := range categories {
		categoryIDs = append(categoryIDs, id)
	}
	sort.Slice(categoryIDs, func(i, j int) bool {
		a, b := categories[categoryIDs[i]], categories[categoryIDs[j]]
		if a.Sort != b.Sort {
			return a.Sort < b.Sort
		}
		return categoryIDs[i] < categoryIDs[j]
	})
	categoryOptions := make([]map[string]any, 0, len(categories))
	allGroups := make([]productReportGroup, 0, len(groups))
	for _, id := range categoryIDs {
		info := categories[id]
		categoryOptions = append(categoryOptions, map[string]any{"id": id, "name": info.Name})
		if group := groups[id]; group != nil {
			sort.Slice(group.Products, func(i, j int) bool { return group.Products[i].ProductID < group.Products[j].ProductID })
			allGroups = append(allGroups, *group)
		}
	}
	limit, offset := pagination(r)
	start := min(offset, len(allGroups))
	end := min(start+limit, len(allGroups))
	result := map[string]any{"year": period.Year, "month": period.Month, "groups": allGroups[start:end], "total": len(allGroups), "limit": limit, "offset": offset, "summary": summary, "options": map[string]any{"categories": categoryOptions, "products": options}, "quantity_basis": "purchase_units_and_renewal_items", "refund_basis": "gross_settled_sales_before_separate_refund_cash_outflows", "export_scope": "all_filtered_groups"}
	if r.URL.Query().Get("format") == "csv" {
		result["products"] = filtered
	}
	writeReport(w, r, "product_groups", result)
}
