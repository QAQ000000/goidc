-- Preserve historical checksums: consolidate receipts with a forward migration.
-- The payment-close, verification and cancellation tables are business facts,
-- not receipts, and retain their state machines, references and guards.
CREATE TABLE mutation_operations (
 actor_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 scope text NOT NULL CHECK (char_length(scope) BETWEEN 1 AND 160),
 idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
 request_hash text NOT NULL CHECK (char_length(request_hash)=64),
 result jsonb NOT NULL CHECK (jsonb_typeof(result)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(actor_id,scope,idempotency_key)
);

-- Block old writers before the copy, not only at DROP, so no receipt can land
-- between the snapshot and removal. Migrate runs this entire file atomically.
LOCK TABLE admin_setting_operations,affiliate_operations,marketing_campaign_operations,
 ticket_mutation_operations,order_operation_receipts,external_refund_operations,
 credit_account_operations,balance_topup_operations,invoice_application_operations,
 manual_invoice_operations,payment_close_anomaly_refund_operations,
 log_cleanup_operations,order_batch_requests IN ACCESS EXCLUSIVE MODE;

-- Prefixes prevent unrelated former tables from sharing a key domain. Actions
-- within one old key domain intentionally still conflict with each other.
INSERT INTO mutation_operations
 SELECT actor_id,'admin:'||operation,idempotency_key,request_hash,result,created_at FROM admin_setting_operations
 UNION ALL
 SELECT user_id,'affiliate:'||operation,idempotency_key,request_hash,result,created_at FROM affiliate_operations
 UNION ALL
 SELECT actor_id,'marketing',idempotency_key,request_hash,result,created_at FROM marketing_campaign_operations
 UNION ALL
 SELECT actor_id,'ticket',idempotency_key,request_hash,result,created_at FROM ticket_mutation_operations
 UNION ALL
 SELECT actor_id,'order:'||action,idempotency_key,request_hash,result,created_at FROM order_operation_receipts
 UNION ALL
 SELECT actor_id,'external-refund',idempotency_key,request_hash,result,created_at FROM external_refund_operations
 UNION ALL
 SELECT user_id,'balance-topup',idempotency_key,request_hash,result,now() FROM balance_topup_operations
 UNION ALL
 SELECT actor_id,'invoice-application',idempotency_key,request_hash,result,created_at FROM invoice_application_operations
 UNION ALL
 SELECT actor_id,'manual-invoice',idempotency_key,request_hash,result,created_at FROM manual_invoice_operations
 UNION ALL
 SELECT actor_id,'payment-anomaly-refund',idempotency_key,request_hash,result,created_at FROM payment_close_anomaly_refund_operations
 UNION ALL
 SELECT actor_id,'log-cleanup',idempotency_key,request_hash,result,created_at FROM log_cleanup_operations
 UNION ALL
 SELECT actor_id,'order-batch:'||action,idempotency_key,request_hash,'{}'::jsonb,created_at FROM order_batch_requests
 UNION ALL
 -- Credit freeze previously compared these fields directly. This canonical
 -- string matches the Go request hash, including untrimmed note text.
 SELECT actor_id,'credit-freeze',idempotency_key,
 encode(sha256(convert_to(user_id::text||':'||CASE WHEN frozen THEN 'true' ELSE 'false' END||':'||expected_version::text||':'||note,'UTF8')),'hex'),
 jsonb_build_object('version',version_after),created_at FROM credit_account_operations;

DROP TABLE admin_setting_operations,affiliate_operations,marketing_campaign_operations,
 ticket_mutation_operations,order_operation_receipts,external_refund_operations,
 credit_account_operations,balance_topup_operations,invoice_application_operations,
 manual_invoice_operations,payment_close_anomaly_refund_operations,
 log_cleanup_operations,order_batch_requests;
