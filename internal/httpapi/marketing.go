package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

// marketingAudience is intentionally small and typed. The JSON snapshot in a
// campaign is normalized from this type before it is persisted or sent.
type marketingAudience struct {
	Mode              string   `json:"mode"`
	Status            string   `json:"status,omitempty"`
	SalesOwnerID      *int64   `json:"sales_owner_id,omitempty"`
	Language          string   `json:"language,omitempty"`
	Country           string   `json:"country,omitempty"`
	RegisteredMinDays *int     `json:"registered_min_days,omitempty"`
	RegisteredMaxDays *int     `json:"registered_max_days,omitempty"`
	Certified         string   `json:"certified,omitempty"`
	PhoneBound        string   `json:"phone_bound,omitempty"`
	EmailBound        string   `json:"email_bound,omitempty"`
	ProductIDs        []int64  `json:"product_ids,omitempty"`
	HostStatuses      []string `json:"host_statuses,omitempty"`
	SupplierIDs       []int64  `json:"supplier_ids,omitempty"`
}

type marketingMutationInput struct {
	Title    string            `json:"title"`
	Mode     string            `json:"mode"`
	Channel  string            `json:"channel"`
	Subject  string            `json:"subject"`
	Body     string            `json:"body"`
	Audience marketingAudience `json:"audience"`
}

func normalizeMarketingAudience(a *marketingAudience) error {
	if a.Mode == "" {
		a.Mode = "customer"
	}
	if a.Mode != "customer" && a.Mode != "product" {
		return &store.Conflict{Code: "invalid_marketing_audience", Message: "推送模式无效"}
	}
	if a.Status == "" {
		a.Status = "all"
	}
	if a.Status != "all" && a.Status != "active" && a.Status != "disabled" {
		return &store.Conflict{Code: "invalid_marketing_audience", Message: "客户状态无效"}
	}
	for name, value := range map[string]string{"certified": a.Certified, "phone_bound": a.PhoneBound, "email_bound": a.EmailBound} {
		if value != "" && value != "all" && value != "yes" && value != "no" {
			return &store.Conflict{Code: "invalid_marketing_audience", Message: name + "筛选无效"}
		}
	}
	if a.RegisteredMinDays != nil && (*a.RegisteredMinDays < 0 || *a.RegisteredMinDays > 36500) {
		return &store.Conflict{Code: "invalid_marketing_audience", Message: "注册时长范围无效"}
	}
	if a.RegisteredMaxDays != nil && (*a.RegisteredMaxDays < 0 || *a.RegisteredMaxDays > 36500) {
		return &store.Conflict{Code: "invalid_marketing_audience", Message: "注册时长范围无效"}
	}
	if a.RegisteredMinDays != nil && a.RegisteredMaxDays != nil && *a.RegisteredMinDays > *a.RegisteredMaxDays {
		return &store.Conflict{Code: "invalid_marketing_audience", Message: "注册时长下限不能大于上限"}
	}
	if a.SalesOwnerID != nil && *a.SalesOwnerID < 1 {
		return &store.Conflict{Code: "invalid_marketing_audience", Message: "销售无效"}
	}
	for _, status := range a.HostStatuses {
		switch status {
		case "pending_payment", "queued", "provisioning", "active", "suspended", "cancelled", "terminated", "failed":
		default:
			return &store.Conflict{Code: "invalid_marketing_audience", Message: "主机状态无效"}
		}
	}
	if a.Mode == "customer" && (len(a.ProductIDs) > 0 || len(a.HostStatuses) > 0 || len(a.SupplierIDs) > 0) {
		return &store.Conflict{Code: "invalid_marketing_audience", Message: "按客户推送不能使用商品筛选"}
	}
	return nil
}

func validateMarketingInput(in *marketingMutationInput) error {
	in.Title, in.Mode, in.Channel = strings.TrimSpace(in.Title), strings.TrimSpace(in.Mode), strings.TrimSpace(in.Channel)
	in.Subject, in.Body = strings.TrimSpace(in.Subject), strings.TrimSpace(in.Body)
	if in.Title == "" || len(in.Title) > 160 || in.Body == "" || len(in.Body) > 100000 {
		return &store.Conflict{Code: "invalid_marketing_campaign", Message: "营销信息标题或正文无效"}
	}
	if in.Channel != "email" && in.Channel != "sms" && in.Channel != "inbox" {
		return &store.Conflict{Code: "invalid_marketing_campaign", Message: "通知渠道无效"}
	}
	if in.Channel != "sms" && (in.Subject == "" || len(in.Subject) > 240) {
		return &store.Conflict{Code: "invalid_marketing_campaign", Message: "标题无效"}
	}
	if in.Channel == "sms" {
		in.Subject = "短信通知"
	}
	return normalizeMarketingAudience(&in.Audience)
}

// marketingAudienceQuery returns a DISTINCT customer query and its arguments.
// It is used both for preview counts and send snapshots so both operations
// apply exactly the same customer scope and filters.
func marketingAudienceQuery(actor int64, a marketingAudience) (string, []any, error) {
	if err := normalizeMarketingAudience(&a); err != nil {
		return "", nil, err
	}
	args := []any{actor}
	where := []string{"u.role='customer'", "admin_customer_allowed($1,u.id)", "coalesce(cp.receive_marketing,true)"}
	from := ` FROM users u LEFT JOIN customer_profiles cp ON cp.customer_id=u.id`
	if a.Mode == "product" {
		from += ` JOIN services sv ON sv.user_id=u.id JOIN products pr ON pr.id=sv.product_id LEFT JOIN supplier_product_bindings spb ON spb.product_id=pr.id`
	}
	add := func(value any, clause string) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if a.Status != "all" {
		add(a.Status, "u.status=$%d")
	}
	if a.SalesOwnerID != nil {
		add(*a.SalesOwnerID, "u.sales_owner_id=$%d")
	}
	if a.Language != "" {
		add(a.Language, "cp.language=$%d")
	}
	if a.Country != "" {
		add(a.Country, "cp.country=$%d")
	}
	if a.RegisteredMinDays != nil {
		add(*a.RegisteredMinDays, "u.created_at <= now() - ($%d * interval '1 day')")
	}
	if a.RegisteredMaxDays != nil {
		add(*a.RegisteredMaxDays, "u.created_at >= now() - ($%d * interval '1 day')")
	}
	if a.Certified == "yes" {
		where = append(where, "EXISTS (SELECT 1 FROM certification_profiles cfp WHERE cfp.user_id=u.id AND cfp.status='approved')")
	} else if a.Certified == "no" {
		where = append(where, "NOT EXISTS (SELECT 1 FROM certification_profiles cfp WHERE cfp.user_id=u.id AND cfp.status='approved')")
	}
	if a.PhoneBound == "yes" {
		where = append(where, "u.phone_verified AND u.phone <> ''")
	} else if a.PhoneBound == "no" {
		where = append(where, "(NOT u.phone_verified OR u.phone='')")
	}
	if a.EmailBound == "yes" {
		where = append(where, "u.email_verified AND u.email <> ''")
	} else if a.EmailBound == "no" {
		where = append(where, "(NOT u.email_verified OR u.email='')")
	}
	if a.Mode == "product" {
		if len(a.ProductIDs) > 0 {
			add(a.ProductIDs, "sv.product_id=ANY($%d::bigint[])")
		}
		if len(a.HostStatuses) > 0 {
			add(a.HostStatuses, "sv.status=ANY($%d::text[])")
		}
		if len(a.SupplierIDs) > 0 {
			add(a.SupplierIDs, "spb.supplier_id=ANY($%d::bigint[])")
		}
	}
	query := `SELECT DISTINCT u.id,u.email,u.phone,u.email_verified,u.phone_verified,u.display_name` + from + " WHERE " + strings.Join(where, " AND ") + " ORDER BY u.id"
	return query, args, nil
}

func marketingHash(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Server) marketingAudienceOptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var owners, products, suppliers, countries, languages []map[string]any
	for query, target := range map[string]*[]map[string]any{
		`SELECT id,display_name,username FROM users WHERE role='admin' AND status='active' AND sales_enabled ORDER BY display_name,id`: &owners,
		`SELECT id,name FROM products WHERE active ORDER BY name,id`:                                                                   &products,
		`SELECT id,name,provider FROM supplier_accounts WHERE active ORDER BY name,id`:                                                 &suppliers,
		`SELECT DISTINCT country AS value,country AS label FROM customer_profiles WHERE country<>'' ORDER BY country`:                  &countries,
		`SELECT DISTINCT language AS value,language AS label FROM customer_profiles WHERE language<>'' ORDER BY language`:              &languages,
	} {
		rows, err := s.db.Pool.Query(ctx, query)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		items, err := pgx.CollectRows(rows, pgx.RowToMap)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		*target = items
		if *target == nil {
			*target = []map[string]any{}
		}
	}
	write(w, http.StatusOK, map[string]any{"owners": owners, "products": products, "suppliers": suppliers, "countries": countries, "languages": languages})
}

func (s *Server) marketingAudienceCount(w http.ResponseWriter, r *http.Request) {
	var a marketingAudience
	if !decode(w, r, &a) {
		return
	}
	query, args, err := marketingAudienceQuery(identityFrom(r).ID, a)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var total int
	if err = s.db.Pool.QueryRow(r.Context(), "SELECT count(*) FROM ("+query+") selected", args...).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"total": total})
}

func (s *Server) marketingCampaigns(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	args := []any{}
	where := []string{"TRUE"}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		args = append(args, "%"+q+"%")
		where = append(where, fmt.Sprintf("(c.title ILIKE $%d OR c.subject ILIKE $%d)", len(args), len(args)))
	}
	for _, key := range []string{"status", "mode", "channel"} {
		value := strings.TrimSpace(r.URL.Query().Get(key))
		if value == "" || value == "all" {
			continue
		}
		allowed := map[string][]string{"status": {"draft", "queued", "completed", "cancelled", "failed"}, "mode": {"customer", "product"}, "channel": {"email", "sms", "inbox"}}[key]
		valid := false
		for _, candidate := range allowed {
			if value == candidate {
				valid = true
				break
			}
		}
		if !valid {
			writeError(w, 400, "invalid_marketing_filter", "营销筛选条件无效")
			return
		}
		args = append(args, value)
		where = append(where, fmt.Sprintf("c.%s=$%d", key, len(args)))
	}
	base := ` FROM marketing_campaigns c JOIN users u ON u.id=c.created_by WHERE ` + strings.Join(where, " AND ")
	s.writeListPage(w, r, "SELECT count(*)"+base,
		`SELECT c.id,c.title,c.mode,c.channel,c.subject,c.status,c.recipient_count,c.version,c.created_at,c.updated_at,c.sent_at,u.display_name AS created_by`+base+` ORDER BY c.id DESC`, args, limit, offset)
}

func (s *Server) marketingCampaignDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	row := s.db.Pool.QueryRow(r.Context(), `SELECT c.id,c.title,c.mode,c.channel,c.subject,c.body,c.audience,c.status,c.recipient_count,c.version,c.created_by,c.created_at,c.updated_at,c.sent_at,u.display_name AS created_by_name,
(SELECT count(*) FROM marketing_campaign_recipients mr WHERE mr.campaign_id=c.id) AS queued_count,
(SELECT count(*) FROM marketing_campaign_recipients mr LEFT JOIN email_outbox e ON e.id=mr.email_outbox_id LEFT JOIN sms_outbox sm ON sm.id=mr.sms_outbox_id WHERE mr.campaign_id=c.id AND coalesce(e.status,sm.status,'sent')='sent') AS sent_count
FROM marketing_campaigns c JOIN users u ON u.id=c.created_by WHERE c.id=$1`, id)
	var audience []byte
	var createdBy string
	// RowToMap cannot be used with QueryRow, so scan the stable fields explicitly.
	var campaignID, createdByID, version, recipientCount, queuedCount, sentCount int64
	var title, mode, channel, subject, body, status string
	var createdAt, updatedAt time.Time
	var sentAt *time.Time
	if err := row.Scan(&campaignID, &title, &mode, &channel, &subject, &body, &audience, &status, &recipientCount, &version, &createdByID, &createdAt, &updatedAt, &sentAt, &createdBy, &queuedCount, &sentCount); err != nil {
		s.fail(w, r, err)
		return
	}
	var out map[string]any
	if err := json.Unmarshal(audience, &out); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"id": campaignID, "title": title, "mode": mode, "channel": channel, "subject": subject, "body": body, "audience": out, "status": status, "recipient_count": recipientCount, "queued_count": queuedCount, "sent_count": sentCount, "version": version, "created_by": createdByID, "created_by_name": createdBy, "created_at": createdAt, "updated_at": updatedAt, "sent_at": sentAt})
}

func (s *Server) createMarketingCampaign(w http.ResponseWriter, r *http.Request) {
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	var in marketingMutationInput
	if !decode(w, r, &in) {
		return
	}
	if err := validateMarketingInput(&in); err != nil {
		s.fail(w, r, err)
		return
	}
	hash, err := marketingHash(in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	audience, _ := json.Marshal(in.Audience)
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if result, replay, e := marketingOperationTx(r.Context(), tx, identityFrom(r).ID, key, hash); e != nil {
			return txResponse{}, e
		} else if replay {

			return txResponse{200, result}, nil
		}
		var id int64
		if err = tx.QueryRow(r.Context(), `INSERT INTO marketing_campaigns(title,mode,channel,subject,body,audience,created_by) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, in.Title, in.Audience.Mode, in.Channel, in.Subject, in.Body, audience, identityFrom(r).ID).Scan(&id); err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "status": "draft", "version": int64(1)}
		if err = saveMarketingOperation(r.Context(), tx, identityFrom(r).ID, key, hash, result); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "marketing_campaign.created", "marketing_campaign", id, map[string]any{"mode": in.Audience.Mode, "channel": in.Channel}); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusCreated, result}, nil
	})
}

func (s *Server) updateMarketingCampaign(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	var in struct {
		marketingMutationInput
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedVersion < 1 {
		writeError(w, 400, "expected_version_required", "请提供活动版本")
		return
	}
	if err := validateMarketingInput(&in.marketingMutationInput); err != nil {
		s.fail(w, r, err)
		return
	}
	hash, err := marketingHash(struct {
		ID    int64
		Input any
	}{id, in})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if result, replay, e := marketingOperationTx(r.Context(), tx, identityFrom(r).ID, key, hash); e != nil {
			return txResponse{}, e
		} else if replay {

			return txResponse{200, result}, nil
		}
		var current int64
		var status string
		if err = tx.QueryRow(r.Context(), "SELECT version,status FROM marketing_campaigns WHERE id=$1 FOR UPDATE", id).Scan(&current, &status); err != nil {
			return txResponse{}, err
		}
		if status != "draft" {
			return txResponse{}, &txHTTPError{409, "marketing_campaign_locked", "已发送的营销活动不能编辑"}
		}
		if current != in.ExpectedVersion {
			return txResponse{}, &txHTTPError{409, "version_conflict", "营销活动已被其他管理员修改，请刷新后重试"}
		}
		audience, _ := json.Marshal(in.Audience)
		if _, err = store.UpdateVersioned(r.Context(), tx, "marketing_campaigns", `title=$1,mode=$2,channel=$3,subject=$4,body=$5,audience=$6`, id, in.ExpectedVersion, in.Title, in.Audience.Mode, in.Channel, in.Subject, in.Body, audience); err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "status": "draft", "version": current + 1}
		if err = saveMarketingOperation(r.Context(), tx, identityFrom(r).ID, key, hash, result); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "marketing_campaign.updated", "marketing_campaign", id, map[string]any{"version": current + 1}); err != nil {
			return txResponse{}, err
		}

		return txResponse{200, result}, nil
	})
}

func marketingOperationTx(ctx context.Context, tx pgx.Tx, actor int64, key, hash string) (map[string]any, bool, error) {
	var out map[string]any
	replayed, err := store.ReplayOperation(ctx, tx, actor, "marketing", key, hash, &out)
	if err == nil && replayed {
		out["replayed"] = true
	}
	return out, replayed, err
}

func saveMarketingOperation(ctx context.Context, tx pgx.Tx, actor int64, key, hash string, result map[string]any) error {
	return store.SaveOperation(ctx, tx, actor, "marketing", key, hash, result)
}

func (s *Server) sendMarketingCampaign(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.ExpectedVersion < 1 {
		writeError(w, 400, "expected_version_required", "请提供活动版本")
		return
	}
	hash, err := marketingHash(struct {
		ID      int64
		Version int64
	}{id, input.ExpectedVersion})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		actor := identityFrom(r).ID
		if result, replay, e := marketingOperationTx(r.Context(), tx, actor, key, hash); e != nil {
			return txResponse{}, e
		} else if replay {

			return txResponse{200, result}, nil
		}
		var title, channel, subject, body, status string
		var audienceRaw []byte
		var version int64
		if err = tx.QueryRow(r.Context(), `SELECT title,channel,subject,body,audience,status,version FROM marketing_campaigns WHERE id=$1 FOR UPDATE`, id).Scan(&title, &channel, &subject, &body, &audienceRaw, &status, &version); err != nil {
			return txResponse{}, err
		}
		if status != "draft" {
			return txResponse{}, &txHTTPError{409, "marketing_campaign_locked", "活动已发送或已取消"}
		}
		if version != input.ExpectedVersion {
			return txResponse{}, &txHTTPError{409, "version_conflict", "营销活动已被其他管理员修改，请刷新后重试"}
		}
		var audience marketingAudience
		if err = json.Unmarshal(audienceRaw, &audience); err != nil {
			return txResponse{}, err
		}
		query, args, err := marketingAudienceQuery(actor, audience)
		if err != nil {
			return txResponse{}, err
		}
		rows, err := tx.Query(r.Context(), query, args...)
		if err != nil {
			return txResponse{}, err
		}
		type recipient struct {
			ID                           int64
			Email, Phone                 string
			EmailVerified, PhoneVerified bool
			Name                         string
		}
		recipients := []recipient{}
		for rows.Next() {
			var item recipient
			if err = rows.Scan(&item.ID, &item.Email, &item.Phone, &item.EmailVerified, &item.PhoneVerified, &item.Name); err != nil {
				rows.Close()
				return txResponse{}, err
			}
			recipients = append(recipients, item)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return txResponse{}, err
		}
		if len(recipients) > 10000 {
			return txResponse{}, &txHTTPError{409, "marketing_recipient_limit", "单次营销推送最多 10000 位客户"}
		}
		if channel == "email" {
			var available bool
			if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM channel_instances WHERE category='mail' AND enabled=true)`).Scan(&available); err != nil {
				return txResponse{}, err
			}
			if !available {
				return txResponse{}, &txHTTPError{http.StatusConflict, "email_unavailable", "邮件渠道未启用"}
			}
		}
		queued := 0
		for _, item := range recipients {
			var emailID, smsID, inboxID *int64
			switch channel {
			case "email":
				if item.Email == "" {
					continue
				}
				var value int64
				if err = tx.QueryRow(r.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind,channel_instance_id) VALUES($1,$2,$3,'marketing',(SELECT id FROM channel_instances WHERE category='mail' AND enabled=true ORDER BY sort_order,id LIMIT 1)) RETURNING id`, item.Email, subject, body).Scan(&value); err != nil {
					return txResponse{}, err
				}
				emailID = &value
			case "sms":
				if !item.PhoneVerified || item.Phone == "" {
					continue
				}
				value, queueErr := s.db.QueueSMSWithID(r.Context(), tx, item.Phone, body, "marketing", "", time.Now().Add(30*24*time.Hour))
				if queueErr != nil {
					return txResponse{}, queueErr
				}
				smsID = &value
			case "inbox":
				var value int64
				if err = tx.QueryRow(r.Context(), `INSERT INTO admin_inbox_messages(user_id,subject,body) VALUES($1,$2,$3) RETURNING id`, item.ID, subject, body).Scan(&value); err != nil {
					return txResponse{}, err
				}
				inboxID = &value
			}
			if emailID == nil && smsID == nil && inboxID == nil {
				continue
			}
			if _, err = tx.Exec(r.Context(), `INSERT INTO marketing_campaign_recipients(campaign_id,user_id,channel,email_outbox_id,sms_outbox_id,inbox_message_id) VALUES($1,$2,$3,$4,$5,$6)`, id, item.ID, channel, emailID, smsID, inboxID); err != nil {
				return txResponse{}, err
			}
			queued++
		}
		newStatus := "queued"
		var sentAt any
		if channel == "inbox" || queued == 0 {
			newStatus = "completed"
			sentAt = time.Now()
		}
		if _, err = tx.Exec(r.Context(), `UPDATE marketing_campaigns SET status=$2,recipient_count=$3,version=version+1,updated_at=now(),sent_at=$4 WHERE id=$1`, id, newStatus, queued, sentAt); err != nil {
			return txResponse{}, err
		}
		result := map[string]any{"id": id, "status": newStatus, "recipient_count": queued, "version": version + 1}
		if err = saveMarketingOperation(r.Context(), tx, actor, key, hash, result); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, actor, "marketing_campaign.sent", "marketing_campaign", id, map[string]any{"channel": channel, "recipient_count": queued}); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusAccepted, result}, nil
	})
}
