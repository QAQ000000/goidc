CREATE TABLE service_cancellation_requests (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  service_id bigint NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  type text NOT NULL CHECK (type IN ('immediate','end_of_billing')),
  reason text NOT NULL CHECK (char_length(reason) BETWEEN 3 AND 2000),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','declined','withdrawn')),
  idempotency_key text NOT NULL,
  resolved_by bigint REFERENCES users(id),
  resolution_note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  UNIQUE(user_id,idempotency_key)
);
CREATE UNIQUE INDEX service_cancellation_pending ON service_cancellation_requests(service_id)
  WHERE status='pending';
CREATE INDEX service_cancellation_status ON service_cancellation_requests(status, id DESC);
CREATE INDEX service_cancellation_user ON service_cancellation_requests(user_id, id DESC);
