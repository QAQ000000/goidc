package store

import (
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

const ticketHandlerReportSQL = `WITH ticket_totals AS (
 SELECT coalesce(t.assignee_id,0) handler_id,count(*)::bigint ticket_count,
 count(*) FILTER(WHERE t.merged_ticket_id IS NULL AND t.status IN ('open','pending'))::bigint open_count,
 count(*) FILTER(WHERE t.status='closed')::bigint closed_count
 FROM tickets t WHERE t.created_at >= $1 AND t.created_at < $2 AND admin_ticket_department_allowed($3,t.department_id)
 AND ($4::bigint IS NULL OR coalesce(t.assignee_id,0)=$4) GROUP BY 1
), public_messages AS (
 SELECT r.author_id handler_id,r.created_at,tr.stars FROM ticket_replies r JOIN tickets t ON t.id=r.ticket_id LEFT JOIN ticket_ratings tr ON tr.reply_id=r.id
 WHERE r.author_kind='staff' AND admin_ticket_department_allowed($3,t.department_id)
 UNION ALL
 SELECT t.created_by,t.created_at,tr.stars FROM tickets t LEFT JOIN ticket_ratings tr ON tr.initial_ticket_id=t.id
 WHERE t.initial_author_kind='staff' AND t.created_by IS NOT NULL AND admin_ticket_department_allowed($3,t.department_id)
), reply_totals AS (
 SELECT handler_id,count(*)::bigint reply_count,count(stars)::bigint rated_reply_count,coalesce(sum(stars),0)::bigint total_score,
 count(*) FILTER(WHERE stars=1)::bigint one_star_count,count(*) FILTER(WHERE stars=2)::bigint two_star_count,
 count(*) FILTER(WHERE stars=3)::bigint three_star_count,count(*) FILTER(WHERE stars=4)::bigint four_star_count,count(*) FILTER(WHERE stars=5)::bigint five_star_count
 FROM public_messages WHERE created_at >= $1 AND created_at < $2 AND ($4::bigint IS NULL OR handler_id=$4) GROUP BY 1
), result AS (
 SELECT coalesce(t.handler_id,r.handler_id) handler_id,coalesce(u.display_name,'未分派') handler_name,
 coalesce(t.ticket_count,0)::bigint ticket_count,coalesce(t.open_count,0)::bigint open_count,coalesce(t.closed_count,0)::bigint closed_count,
 coalesce(r.reply_count,0)::bigint reply_count,coalesce(r.rated_reply_count,0)::bigint rated_reply_count,coalesce(r.total_score,0)::bigint total_score,
 coalesce(r.one_star_count,0)::bigint one_star_count,coalesce(r.two_star_count,0)::bigint two_star_count,coalesce(r.three_star_count,0)::bigint three_star_count,coalesce(r.four_star_count,0)::bigint four_star_count,coalesce(r.five_star_count,0)::bigint five_star_count
 FROM ticket_totals t FULL JOIN reply_totals r ON r.handler_id=t.handler_id LEFT JOIN administrators u ON u.id=coalesce(t.handler_id,r.handler_id)
) `

func (s *Store) TicketHandlerReport(ctx context.Context, actor int64, start, end time.Time, handler *int64, sort, direction string, limit, offset int) (map[string]any, error) {
	allowed := map[string]bool{"handler_id": true, "handler_name": true, "ticket_count": true, "open_count": true, "closed_count": true, "reply_count": true, "rated_reply_count": true, "total_score": true, "one_star_count": true, "two_star_count": true, "three_star_count": true, "four_star_count": true, "five_star_count": true}
	if !allowed[sort] || (direction != "asc" && direction != "desc") {
		return nil, conflict("invalid_report_sort", "排序字段或方向无效")
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var total int64
	if err = tx.QueryRow(ctx, ticketHandlerReportSQL+"SELECT count(*) FROM result", start, end, actor, handler).Scan(&total); err != nil {
		return nil, err
	}
	items, err := collectMaps(tx.Query(ctx, ticketHandlerReportSQL+"SELECT * FROM result ORDER BY "+sort+" "+direction+",handler_id LIMIT $5 OFFSET $6", start, end, actor, handler, limit, offset))
	if err != nil {
		return nil, err
	}
	options, err := collectMaps(tx.Query(ctx, ticketHandlerReportSQL+"SELECT handler_id,handler_name FROM result ORDER BY handler_name,handler_id", start, end, actor, nil))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"total": total, "items": items, "limit": limit, "offset": offset, "handlers": options}, nil
}
