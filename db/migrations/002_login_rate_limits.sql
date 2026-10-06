CREATE TABLE auth_rate_limits (
  key text PRIMARY KEY,
  attempts int NOT NULL DEFAULT 0,
  window_start timestamptz NOT NULL DEFAULT now()
);
