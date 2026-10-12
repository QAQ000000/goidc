ALTER TABLE customer_credit_accounts ADD COLUMN frozen boolean NOT NULL DEFAULT false;
CREATE TABLE credit_invoice_payments (
 invoice_id bigint PRIMARY KEY REFERENCES invoices(id), user_id bigint NOT NULL REFERENCES users(id),
 amount_cents bigint NOT NULL CHECK(amount_cents>0), remaining_cents bigint NOT NULL CHECK(remaining_cents>=0),
 status text NOT NULL DEFAULT 'outstanding' CHECK(status IN ('outstanding','repaid','refunded')),
 version bigint NOT NULL DEFAULT 1, expected_version bigint NOT NULL, idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 128),
 created_at timestamptz NOT NULL DEFAULT now(), refunded_at timestamptz,
 UNIQUE(user_id,idempotency_key),CHECK(remaining_cents<=amount_cents)
);
CREATE TABLE credit_repayments (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, invoice_id bigint NOT NULL REFERENCES credit_invoice_payments(invoice_id),
 user_id bigint NOT NULL REFERENCES users(id), actor_id bigint NOT NULL REFERENCES users(id),
 amount_cents bigint NOT NULL CHECK(amount_cents>0), receipt text NOT NULL UNIQUE CHECK(length(receipt) BETWEEN 3 AND 240),
 received_at timestamptz NOT NULL, note text NOT NULL DEFAULT '', idempotency_key text NOT NULL,
 expected_version bigint NOT NULL, version_after bigint NOT NULL, remaining_after bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(actor_id,idempotency_key)
);
CREATE TABLE credit_account_operations (
 actor_id bigint NOT NULL REFERENCES users(id), idempotency_key text NOT NULL,
 user_id bigint NOT NULL REFERENCES users(id), frozen boolean NOT NULL, expected_version bigint NOT NULL,
 version_after bigint NOT NULL,note text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(actor_id,idempotency_key)
);
CREATE INDEX credit_invoice_payments_user ON credit_invoice_payments(user_id,created_at DESC);
ALTER TABLE credit_invoice_payments ADD COLUMN refund_key text, ADD COLUMN refund_actor_id bigint REFERENCES users(id);
CREATE UNIQUE INDEX credit_refund_key ON credit_invoice_payments(refund_key) WHERE refund_key IS NOT NULL;
