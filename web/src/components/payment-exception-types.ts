export type ExceptionPage<T> = { items: T[]; total: number; limit: number; offset: number };
export type CloseResolution = { verification_id: number; decision_id: number; close_operation_id: number; operation_id: string; status: "applied_closed" | "applied_paid" | "already_paid" | "superseded_paid"; payment_status: string; version: number; external_transaction_id: string };
export type CloseVerification = {
  id: number; close_operation_id: number; payment_id: number; invoice_id: number;
  requested_by: number; original_actor_id: number; provider: string; provider_payment_id: string;
  operation_id: string; amount_cents: number; expected_version: number; requested_at: string; lease_until: string;
  /** SHA-256 of this employee's original lookup idempotency key; never the key itself. */
  request_key_sha256?: string;
  result_status: string; result_code: string; external_transaction_id: string; provider_evidence_id: string;
  received_at: string | null; review_status: string; review_note: string; reviewed_by: number | null; reviewed_at: string | null;
  version: number; payment_status: string; invoice_status: string; status: string; replayed: boolean;
  can_confirm: boolean; can_reject: boolean;
};
export type CloseVerificationPage = ExceptionPage<CloseVerification> & {
  invoice_id: number; payment_id: number; version: number; lookup_supported: boolean; can_request: boolean;
  operations: { id: number; operation_id: string; status: string; lease_until: string; can_verify: boolean }[];
  effective_resolution: CloseResolution | null;
};
export type CloseVerificationReceipt = { verification: CloseVerification; resolution: CloseResolution | null };
export type AnomalyRefund = {
  id: number; anchor_anomaly_id: number; payment_id: number; invoice_id: number; user_id: number;
  provider: string; channel_instance_id: string; provider_payment_id: string; external_transaction_id: string;
  amount_cents: number; expected_group_version: number; status: "pending_review" | "confirmed" | "rejected";
  version: number; method: string; provider_refund_id: string; received_at: string; evidence_ref: string;
  evidence_sha256: string; note: string; requested_by: number; reviewed_by: number | null; review_note: string;
  reviewed_group_version: number | null; confirmed_at: string | null; created_at: string; updated_at: string;
  can_review: boolean;
};
export type AnomalyGroup = {
  anchor_anomaly_id: number; group_version: number; payment_id: number; invoice_id: number; user_id: number;
  provider: string; provider_payment_id: string; external_transaction_id: string; amount_cents: number;
  event_count: number; first_seen: string; last_seen: string; merchant_verified: boolean; unresolved: boolean;
  status: string; can_create: boolean; refunds: AnomalyRefund[];
};
export type AnomalyReceipt = { refund: AnomalyRefund; group: AnomalyGroup; invoice_has_unresolved: boolean; replayed: boolean };
export type AnomalySummary = { anomaly_received_cents: number; anomaly_refunded_cents: number; anomaly_pending_cents: number; unresolved_groups: number; unverified_groups: number; unverified_amount_cents: number; currency: string };
export type AnomalyEntry = { event_key: string; source: string; direction: "income" | "expense" | "neutral"; anchor_anomaly_id: number; payment_id: number; refund_id: number | null; invoice_id: number; amount_cents: number; transaction_no: string; occurred_at: string; verified: boolean };
