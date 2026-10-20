package store

import "context"

func (s *Store) AdministratorCertificationHistory(ctx context.Context, actor, id int64) ([]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `SELECT r.id,r.submission_id,r.reviewer_id,coalesce(u.display_name,u.username) AS reviewer_name,r.decision,r.note,r.created_at
FROM certification_reviews r JOIN administrators u ON u.id=r.reviewer_id JOIN certification_profiles p ON p.id=r.profile_id
WHERE r.profile_id=$1 AND admin_customer_allowed($2,p.user_id) ORDER BY r.id DESC`, id, actor)
	return collectMaps(rows, err)
}
