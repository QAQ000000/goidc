-- One settled invoice contributes once, including invoices earning no commission.
CREATE TABLE affiliate_turnover_entries (
 invoice_id bigint PRIMARY KEY REFERENCES invoices(id),
 account_id bigint NOT NULL REFERENCES affiliate_accounts(id),
 rule_set_id bigint NOT NULL REFERENCES affiliate_rule_sets(id),
 basis_cents bigint NOT NULL CHECK(basis_cents>=0),
 paid_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX affiliate_turnover_account_window ON affiliate_turnover_entries(account_id,paid_at);
-- Establish the historical turnover baseline without generating/recomputing any
-- commissions. Only invoices with attributable sale/renewal/upgrade lines count.
INSERT INTO affiliate_turnover_entries(invoice_id,account_id,rule_set_id,basis_cents,paid_at)
SELECT i.id,a.id,rs.id,
 CASE WHEN i.order_id IS NOT NULL THEN
  LEAST(i.amount_cents,COALESCE((SELECT sum(oi.total_cents) FROM order_items oi WHERE oi.order_id=i.order_id),0))
 ELSE COALESCE((SELECT sum(ii.amount_cents) FROM invoice_items ii JOIN services s ON s.id=ii.service_id
  WHERE ii.invoice_id=i.id AND ii.item_type IN ('renew','upgrade') AND s.user_id=i.user_id),0) END,
 COALESCE((SELECT received_at FROM credit_repayments cr WHERE cr.invoice_id=i.id AND cr.remaining_after=0 ORDER BY version_after DESC LIMIT 1),i.paid_at)
FROM invoices i JOIN affiliate_attributions at ON at.user_id=i.user_id
JOIN affiliate_accounts a ON a.id=at.account_id
JOIN affiliate_rule_sets rs ON rs.program_id=a.program_id AND rs.effective_from<=i.created_at
 AND (rs.effective_to IS NULL OR rs.effective_to>i.created_at)
WHERE i.status='paid' AND i.paid_at IS NOT NULL AND i.amount_cents>=0
 AND a.user_id<>i.user_id AND at.status='active' AND at.registered_at<=i.created_at
 AND NOT EXISTS(SELECT 1 FROM orders o WHERE o.id=i.order_id AND o.status IN ('cancelled','refunded'))
 AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments c WHERE c.invoice_id=i.id AND c.status<>'repaid')
 AND (EXISTS(SELECT 1 FROM order_items oi WHERE oi.order_id=i.order_id)
 OR EXISTS(SELECT 1 FROM invoice_items ii JOIN services s ON s.id=ii.service_id WHERE ii.invoice_id=i.id AND ii.item_type IN ('renew','upgrade') AND s.user_id=i.user_id));
