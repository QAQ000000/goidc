package httpapi

import (
	"encoding/json"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

const contactColumns = `c.id,c.customer_id,c.username,c.sex,c.avatar,c.company_name,c.email,c.wechat_id,
c.country,c.province,c.city,c.region,c.address1,c.address2,c.postcode,c.phone_number,c.permissions,
c.general_emails,c.invoice_emails,c.product_emails,c.support_emails,c.status,c.created_at,c.updated_at`

type contactInput struct {
	Username      string   `json:"username"`
	Sex           int16    `json:"sex"`
	Avatar        string   `json:"avatar"`
	CompanyName   string   `json:"company_name"`
	Email         string   `json:"email"`
	WechatID      string   `json:"wechat_id"`
	Country       string   `json:"country"`
	Province      string   `json:"province"`
	City          string   `json:"city"`
	Region        string   `json:"region"`
	Address1      string   `json:"address1"`
	Address2      string   `json:"address2"`
	Postcode      string   `json:"postcode"`
	PhoneNumber   string   `json:"phone_number"`
	Permissions   []string `json:"permissions"`
	GeneralEmails bool     `json:"general_emails"`
	InvoiceEmails bool     `json:"invoice_emails"`
	ProductEmails bool     `json:"product_emails"`
	SupportEmails bool     `json:"support_emails"`
	Status        string   `json:"status"`
}

type contactPatch struct {
	Username      *string   `json:"username"`
	Sex           *int16    `json:"sex"`
	Avatar        *string   `json:"avatar"`
	CompanyName   *string   `json:"company_name"`
	Email         *string   `json:"email"`
	WechatID      *string   `json:"wechat_id"`
	Country       *string   `json:"country"`
	Province      *string   `json:"province"`
	City          *string   `json:"city"`
	Region        *string   `json:"region"`
	Address1      *string   `json:"address1"`
	Address2      *string   `json:"address2"`
	Postcode      *string   `json:"postcode"`
	PhoneNumber   *string   `json:"phone_number"`
	Permissions   *[]string `json:"permissions"`
	GeneralEmails *bool     `json:"general_emails"`
	InvoiceEmails *bool     `json:"invoice_emails"`
	ProductEmails *bool     `json:"product_emails"`
	SupportEmails *bool     `json:"support_emails"`
	Status        *string   `json:"status"`
}

func normalizeContactInput(input *contactInput) error {
	if input.Permissions == nil {
		input.Permissions = []string{}
	}
	input.Username = strings.TrimSpace(input.Username)
	input.Avatar = strings.TrimSpace(input.Avatar)
	input.CompanyName = strings.TrimSpace(input.CompanyName)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.WechatID = strings.TrimSpace(input.WechatID)
	input.Country = strings.TrimSpace(input.Country)
	input.Province = strings.TrimSpace(input.Province)
	input.City = strings.TrimSpace(input.City)
	input.Region = strings.TrimSpace(input.Region)
	input.Address1 = strings.TrimSpace(input.Address1)
	input.Address2 = strings.TrimSpace(input.Address2)
	input.Postcode = strings.TrimSpace(input.Postcode)
	input.PhoneNumber = strings.TrimSpace(input.PhoneNumber)
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	if input.Status == "" {
		input.Status = "inactive"
	}
	if input.Email == "" || len(input.Email) > 254 {
		return &store.Conflict{Code: "invalid_contact", Message: "联系人邮箱无效"}
	}
	parsed, err := mail.ParseAddress(input.Email)
	if err != nil || parsed.Address != input.Email {
		return &store.Conflict{Code: "invalid_contact", Message: "联系人邮箱无效"}
	}
	if input.Sex < 0 || input.Sex > 2 || input.Status != "active" && input.Status != "inactive" && input.Status != "disabled" {
		return &store.Conflict{Code: "invalid_contact", Message: "联系人字段无效"}
	}
	if len(input.Username) > 120 || len(input.CompanyName) > 160 || len(input.PhoneNumber) > 80 || len(input.Permissions) > 50 {
		return &store.Conflict{Code: "invalid_contact", Message: "联系人字段长度无效"}
	}
	for i, permission := range input.Permissions {
		input.Permissions[i] = strings.TrimSpace(permission)
		if input.Permissions[i] == "" || len(input.Permissions[i]) > 80 {
			return &store.Conflict{Code: "invalid_contact", Message: "联系人权限无效"}
		}
	}
	return nil
}

func (s *Server) contacts(w http.ResponseWriter, r *http.Request) {
	customerID, ok := contactCustomerID(w, r)
	if !ok {
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if adminRoute(r) {
		if err = store.LockAdminCustomer(r.Context(), tx, identityFrom(r).ID, customerID); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	rows, err := tx.Query(r.Context(), `SELECT `+contactColumns+` FROM customer_contacts c WHERE c.customer_id=$1 ORDER BY c.id DESC`, customerID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	write(w, 200, items)
}

func contactCustomerID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if adminRoute(r) {
		return pathID(w, r)
	}
	return identityFrom(r).ID, true
}

func contactPathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	value := r.PathValue("id")
	if r.PathValue("contact_id") != "" {
		value = r.PathValue("contact_id")
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid_id", "记录编号无效")
		return 0, false
	}
	return id, true
}

func (s *Server) createContact(w http.ResponseWriter, r *http.Request) {
	customerID, ok := contactCustomerID(w, r)
	if !ok {
		return
	}
	var input contactInput
	if !decode(w, r, &input) {
		return
	}
	if err := normalizeContactInput(&input); err != nil {
		s.fail(w, r, err)
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if adminRoute(r) {
			if err = store.LockAdminCustomer(r.Context(), tx, identityFrom(r).ID, customerID); err != nil {
				return txResponse{}, err
			}
		}
		var id int64
		if err = s.ensureContactEmail(r, tx, input.Email, 0); err != nil {
			return txResponse{}, err
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO customer_contacts(customer_id,username,sex,avatar,company_name,email,wechat_id,country,province,city,region,address1,address2,postcode,phone_number,permissions,general_emails,invoice_emails,product_emails,support_emails,status)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21) RETURNING id`, customerID, input.Username, input.Sex, input.Avatar, input.CompanyName, input.Email, input.WechatID, input.Country, input.Province, input.City, input.Region, input.Address1, input.Address2, input.Postcode, input.PhoneNumber, input.Permissions, input.GeneralEmails, input.InvoiceEmails, input.ProductEmails, input.SupportEmails, input.Status).Scan(&id)
		if err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "customer_contact.created", "customer_contact", id, map[string]any{"customer_id": customerID, "email": input.Email}); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusCreated, map[string]any{"id": id, "customer_id": customerID}}, nil
	})
}

func (s *Server) ensureContactEmail(r *http.Request, tx pgx.Tx, email string, contactID int64) error {
	var exists bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=lower($1)) OR EXISTS(SELECT 1 FROM customer_contacts WHERE lower(email)=lower($1) AND id<>$2)`, email, contactID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return &store.Conflict{Code: "contact_email_exists", Message: "该邮箱已被使用"}
	}
	return nil
}

func (s *Server) updateContact(w http.ResponseWriter, r *http.Request) {
	id, ok := contactPathID(w, r)
	if !ok {
		return
	}
	customerID, ok := contactCustomerID(w, r)
	if !ok {
		return
	}
	var input contactPatch
	if !decode(w, r, &input) {
		return
	}
	if input.Email != nil {
		*input.Email = strings.ToLower(strings.TrimSpace(*input.Email))
		parsed, err := mail.ParseAddress(*input.Email)
		if err != nil || parsed.Address != *input.Email || len(*input.Email) > 254 {
			writeError(w, 400, "invalid_contact", "联系人邮箱无效")
			return
		}
	}
	if input.Username != nil {
		*input.Username = strings.TrimSpace(*input.Username)
	}
	if input.CompanyName != nil {
		*input.CompanyName = strings.TrimSpace(*input.CompanyName)
	}
	if input.PhoneNumber != nil {
		*input.PhoneNumber = strings.TrimSpace(*input.PhoneNumber)
	}
	for _, field := range []*string{input.Avatar, input.WechatID, input.Country, input.Province, input.City, input.Region, input.Address1, input.Address2, input.Postcode} {
		if field != nil {
			*field = strings.TrimSpace(*field)
		}
	}
	if input.Status != nil && *input.Status != "active" && *input.Status != "inactive" && *input.Status != "disabled" {
		writeError(w, 400, "invalid_contact", "联系人状态无效")
		return
	}
	if input.Sex != nil && (*input.Sex < 0 || *input.Sex > 2) {
		writeError(w, 400, "invalid_contact", "联系人性别无效")
		return
	}
	if input.Username != nil && len(*input.Username) > 120 || input.CompanyName != nil && len(*input.CompanyName) > 160 || input.PhoneNumber != nil && len(*input.PhoneNumber) > 80 {
		writeError(w, 400, "invalid_contact", "联系人字段长度无效")
		return
	}
	if input.Permissions != nil {
		if len(*input.Permissions) > 50 {
			writeError(w, 400, "invalid_contact", "联系人权限无效")
			return
		}
		for i, permission := range *input.Permissions {
			(*input.Permissions)[i] = strings.TrimSpace(permission)
			if (*input.Permissions)[i] == "" || len((*input.Permissions)[i]) > 80 {
				writeError(w, 400, "invalid_contact", "联系人权限无效")
				return
			}
		}
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if adminRoute(r) {
			if err = store.LockAdminCustomer(r.Context(), tx, identityFrom(r).ID, customerID); err != nil {
				return txResponse{}, err
			}
		}
		var updated int64
		if err = tx.QueryRow(r.Context(), "SELECT id FROM customer_contacts WHERE id=$1 AND customer_id=$2 FOR UPDATE", id, customerID).Scan(&updated); err != nil {
			return txResponse{}, err
		}
		if input.Email != nil {
			if err = s.ensureContactEmail(r, tx, *input.Email, id); err != nil {
				return txResponse{}, err
			}
		}
		err = tx.QueryRow(r.Context(), `UPDATE customer_contacts SET username=COALESCE($3,username),sex=COALESCE($4,sex),avatar=COALESCE($5,avatar),company_name=COALESCE($6,company_name),email=COALESCE($7,email),wechat_id=COALESCE($8,wechat_id),country=COALESCE($9,country),province=COALESCE($10,province),city=COALESCE($11,city),region=COALESCE($12,region),address1=COALESCE($13,address1),address2=COALESCE($14,address2),postcode=COALESCE($15,postcode),phone_number=COALESCE($16,phone_number),permissions=COALESCE($17,permissions),general_emails=COALESCE($18,general_emails),invoice_emails=COALESCE($19,invoice_emails),product_emails=COALESCE($20,product_emails),support_emails=COALESCE($21,support_emails),status=COALESCE($22,status),updated_at=now() WHERE id=$1 AND customer_id=$2 RETURNING id`, id, customerID, input.Username, input.Sex, input.Avatar, input.CompanyName, input.Email, input.WechatID, input.Country, input.Province, input.City, input.Region, input.Address1, input.Address2, input.Postcode, input.PhoneNumber, input.Permissions, input.GeneralEmails, input.InvoiceEmails, input.ProductEmails, input.SupportEmails, input.Status).Scan(&updated)
		if err != nil {
			if err == pgx.ErrNoRows {
				return txResponse{}, &txHTTPError{404, "not_found", "联系人不存在"}
			}
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "customer_contact.updated", "customer_contact", id, map[string]any{"customer_id": customerID}); err != nil {
			return txResponse{}, err
		}

		return txResponse{200, map[string]any{"id": updated}}, nil
	})
}

func (s *Server) deleteContact(w http.ResponseWriter, r *http.Request) {
	id, ok := contactPathID(w, r)
	if !ok {
		return
	}
	customerID, ok := contactCustomerID(w, r)
	if !ok {
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if adminRoute(r) {
			if err = store.LockAdminCustomer(r.Context(), tx, identityFrom(r).ID, customerID); err != nil {
				return txResponse{}, err
			}
		}
		var deleted int64
		if err = tx.QueryRow(r.Context(), "DELETE FROM customer_contacts WHERE id=$1 AND customer_id=$2 RETURNING id", id, customerID).Scan(&deleted); err != nil {
			if err == pgx.ErrNoRows {
				return txResponse{}, &txHTTPError{404, "not_found", "联系人不存在"}
			}
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "customer_contact.deleted", "customer_contact", id, map[string]any{"customer_id": customerID}); err != nil {
			return txResponse{}, err
		}

		return txResponse{200, map[string]any{"id": deleted, "deleted": true}}, nil
	})
}

func (s *Server) customerGroups(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	where := `($1='' OR g.name ILIKE '%'||$1||'%' OR g.id::text=$1)`
	if r.URL.Query().Get("options") == "true" {
		s.list(w, r, `SELECT g.id,g.name,g.color,g.discount_percent,g.suspend_delete_exempt,g.separate_invoices,g.version,g.created_at,g.updated_at,count(m.customer_id)::int AS member_count FROM customer_groups g LEFT JOIN customer_group_members m ON m.group_id=g.id GROUP BY g.id ORDER BY g.id DESC`)
		return
	}
	limit, offset := pagination(r)
	s.writeListPage(w, r, `SELECT count(*) FROM customer_groups g WHERE `+where,
		`SELECT g.id,g.name,g.color,g.discount_percent,g.suspend_delete_exempt,g.separate_invoices,g.version,g.created_at,g.updated_at,count(m.customer_id)::int AS member_count FROM customer_groups g LEFT JOIN customer_group_members m ON m.group_id=g.id WHERE `+where+` GROUP BY g.id ORDER BY g.id DESC`, []any{q}, limit, offset)
}

func (s *Server) createCustomerGroup(w http.ResponseWriter, r *http.Request) {
	var input store.CustomerGroupCreateInput
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Color = strings.TrimSpace(input.Color)
	if input.Color == "" {
		input.Color = "#64748b"
	}
	if input.Name == "" || len(input.Name) > 120 || len(input.Color) != 7 || input.Color[0] != '#' || input.DiscountPercent < 0 || input.DiscountPercent > 100 {
		writeError(w, 400, "invalid_customer_group", "客户分组字段无效")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	out, replay, err := s.db.CreateAdminCustomerGroup(requestFileContext(r), identityFrom(r).ID, key, input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	write(w, status, out)
}

func (s *Server) updateCustomerGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input store.CustomerGroupPatchInput
	if !decode(w, r, &input) {
		return
	}
	if input.Name != nil {
		*input.Name = strings.TrimSpace(*input.Name)
		if *input.Name == "" || len(*input.Name) > 120 {
			writeError(w, 400, "invalid_customer_group", "分组名称无效")
			return
		}
	}
	if input.Color != nil {
		*input.Color = strings.TrimSpace(*input.Color)
		if len(*input.Color) != 7 || (*input.Color)[0] != '#' {
			writeError(w, 400, "invalid_customer_group", "分组颜色无效")
			return
		}
	}
	if input.DiscountPercent != nil && (*input.DiscountPercent < 0 || *input.DiscountPercent > 100) {
		writeError(w, 400, "invalid_customer_group", "折扣比例无效")
		return
	}
	if input.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "expected_version_required", "请提供有效版本")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	out, err := s.db.UpdateAdminCustomerGroup(requestFileContext(r), identityFrom(r).ID, id, key, input)
	if err != nil {
		s.failCustomerPricing(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) deleteCustomerGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input store.CustomerGroupDeleteInput
	if !decode(w, r, &input) {
		return
	}
	if input.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "expected_version_required", "请提供有效版本")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	out, err := s.db.DeleteAdminCustomerGroup(requestFileContext(r), identityFrom(r).ID, id, key, input)
	if err != nil {
		s.failCustomerPricing(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) assignCustomerGroup(w http.ResponseWriter, r *http.Request) {
	customerID, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		GroupID         json.RawMessage `json:"group_id"`
		ExpectedVersion int64           `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	in := store.AdminCustomerGroupAssignmentInput{ExpectedVersion: input.ExpectedVersion, GroupIDPresent: input.GroupID != nil}
	if input.GroupID != nil && json.Unmarshal(input.GroupID, &in.GroupID) != nil {
		writeError(w, http.StatusBadRequest, "invalid_group", "客户分组必须为正整数或 null")
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) > 1 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "请提供单个幂等键")
		return
	}
	out, err := s.db.AssignAdminCustomerGroup(requestFileContext(r), identityFrom(r).ID, customerID, r.Header.Get("Idempotency-Key"), in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, out)
}
