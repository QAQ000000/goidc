package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

func (s *Store) MarketingAccountAudienceOptions(ctx context.Context) (map[string]any, error) {
	var owners, products, suppliers, countries, languages []map[string]any
	for query, target := range map[string]*[]map[string]any{
		`SELECT id,display_name,username FROM administrators WHERE role='admin' AND status='active' AND sales_enabled ORDER BY display_name,id`: &owners,
		`SELECT id,name FROM products WHERE active ORDER BY name,id`:                                                                            &products,
		`SELECT id,name,provider FROM supplier_accounts WHERE active ORDER BY name,id`:                                                          &suppliers,
		`SELECT DISTINCT country AS value,country AS label FROM customer_profiles WHERE country<>'' ORDER BY country`:                           &countries,
		`SELECT DISTINCT language AS value,language AS label FROM customer_profiles WHERE language<>'' ORDER BY language`:                       &languages,
	} {
		rows, err := s.Pool.Query(ctx, query)
		if err != nil {
			return nil, err
		}
		items, err := pgx.CollectRows(rows, pgx.RowToMap)
		if err != nil {
			return nil, err
		}
		*target = items
		if *target == nil {
			*target = []map[string]any{}
		}
	}
	return map[string]any{"owners": owners, "products": products, "suppliers": suppliers, "countries": countries, "languages": languages}, nil
}
func (s *Store) MarketingAccountCampaignPage(ctx context.Context, q string, filters map[string]string, limit, offset int) (map[string]any, error) {
	args := []any{}
	where := []string{"TRUE"}
	if q != "" {
		args = append(args, "%"+q+"%")
		where = append(where, fmt.Sprintf("(c.title ILIKE $%d OR c.subject ILIKE $%d)", len(args), len(args)))
	}
	for _, key := range []string{"status", "mode", "channel"} {
		if value := filters[key]; value != "" {
			args = append(args, value)
			where = append(where, fmt.Sprintf("c.%s=$%d", key, len(args)))
		}
	}
	base := ` FROM marketing_campaigns c JOIN administrators u ON u.id=c.created_by WHERE ` + strings.Join(where, " AND ")
	return ticketAccountPage(ctx, s.Pool, `SELECT c.id,c.title,c.mode,c.channel,c.subject,c.status,c.recipient_count,c.version,c.created_at,c.updated_at,c.sent_at,u.display_name AS created_by`+base, "id DESC", args, limit, offset)
}
func (s *Store) MarketingAccountCampaignDetail(ctx context.Context, id int64) (map[string]any, error) {
	row := s.Pool.QueryRow(ctx, `SELECT c.id,c.title,c.mode,c.channel,c.subject,c.body,c.audience,c.status,c.recipient_count,c.version,c.created_by,c.created_at,c.updated_at,c.sent_at,u.display_name AS created_by_name,
(SELECT count(*) FROM marketing_campaign_recipients mr WHERE mr.campaign_id=c.id) AS queued_count,
(SELECT count(*) FROM marketing_campaign_recipients mr LEFT JOIN email_outbox e ON e.id=mr.email_outbox_id LEFT JOIN sms_outbox sm ON sm.id=mr.sms_outbox_id WHERE mr.campaign_id=c.id AND coalesce(e.status,sm.status,'sent')='sent') AS sent_count
FROM marketing_campaigns c JOIN administrators u ON u.id=c.created_by WHERE c.id=$1`, id)
	var audience []byte
	var createdBy string
	// RowToMap cannot be used with QueryRow, so scan the stable fields explicitly.
	var campaignID, createdByID, version, recipientCount, queuedCount, sentCount int64
	var title, mode, channel, subject, body, status string
	var createdAt, updatedAt time.Time
	var sentAt *time.Time
	if err := row.Scan(&campaignID, &title, &mode, &channel, &subject, &body, &audience, &status, &recipientCount, &version, &createdByID, &createdAt, &updatedAt, &sentAt, &createdBy, &queuedCount, &sentCount); err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(audience, &out); err != nil {
		return nil, err
	}
	return map[string]any{"id": campaignID, "title": title, "mode": mode, "channel": channel, "subject": subject, "body": body, "audience": out, "status": status, "recipient_count": recipientCount, "queued_count": queuedCount, "sent_count": sentCount, "version": version, "created_by": createdByID, "created_by_name": createdBy, "created_at": createdAt, "updated_at": updatedAt, "sent_at": sentAt}, nil
}
