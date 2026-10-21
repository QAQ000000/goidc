package store

import (
	"context"
	"encoding/json"
	"time"
)

type WebPushHistoryVariable struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
}

type WebPushHistoryItem struct {
	ID        int64                    `json:"id"`
	Kind      string                   `json:"event_kind"`
	Name      string                   `json:"name"`
	CreatedAt time.Time                `json:"created_at"`
	Variables []WebPushHistoryVariable `json:"variables"`
}

// WebPushNotificationHistory exposes business facts associated with an
// accepted or uncertain push, independently of inbox subscriptions. Resource
// ownership is checked when reading: transferring a resource must not leave
// its old owner's event snapshot visible through this history.
func (s *Store) WebPushNotificationHistory(ctx context.Context, userID int64, limit, offset int) ([]WebPushHistoryItem, error) {
	// HTTP caps visible pages at 100 and may request one extra row to determine
	// has_more. Neither pagination nor this read changes notification consent.
	if limit < 1 || limit > 101 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.Pool.Query(ctx, `SELECT e.id,e.kind,e.created_at,e.payload
 FROM notification_events e JOIN account_identities u ON u.id=e.user_id
 WHERE e.user_id=$1 AND u.status='active'
 AND EXISTS(SELECT 1 FROM notification_deliveries d WHERE d.event_id=e.id AND d.channel='web_push' AND d.status IN ('accepted','unknown'))
 AND (e.invoice_id IS NULL OR EXISTS(SELECT 1 FROM invoices i WHERE i.id=e.invoice_id AND i.user_id=$1))
 AND (e.order_id IS NULL OR EXISTS(SELECT 1 FROM orders o WHERE o.id=e.order_id AND o.user_id=$1))
 AND (e.service_id IS NULL OR EXISTS(SELECT 1 FROM services s WHERE s.id=e.service_id AND s.user_id=$1
  AND coalesce(e.payload->>'ownership_generation','0')=service_ownership_generation(s.id)::text))
 ORDER BY e.id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebPushHistoryItem{}
	for rows.Next() {
		var item WebPushHistoryItem
		var payload []byte
		if err = rows.Scan(&item.ID, &item.Kind, &item.CreatedAt, &payload); err != nil {
			return nil, err
		}
		event, ok := notificationEventDescriptor(item.Kind)
		if !ok {
			continue
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(payload, &fields); err != nil {
			return nil, err
		}
		item.Name = event.Name
		item.Variables = []WebPushHistoryVariable{}
		for _, variable := range event.Variables {
			var value string
			// Read only the event's public vocabulary, never payload's internal
			// deadline, session, delivery or provider metadata.
			if raw, exists := fields[variable.Key]; exists && json.Unmarshal(raw, &value) == nil {
				item.Variables = append(item.Variables, WebPushHistoryVariable{Key: variable.Key, Label: variable.Label, Value: value})
			}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
