CREATE TABLE certification_profiles (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
  certification_type text NOT NULL DEFAULT 'individual' CHECK (certification_type IN ('individual','company')),
  status text NOT NULL DEFAULT 'unverified' CHECK (status IN ('unverified','pending','approved','rejected')),
  provider_id text NOT NULL DEFAULT 'manual',
  provider_subject text NOT NULL DEFAULT '',
  rejection_reason text NOT NULL DEFAULT '',
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX certification_profiles_status ON certification_profiles(status,updated_at DESC);

CREATE TABLE certification_submissions (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  profile_id bigint NOT NULL REFERENCES certification_profiles(id) ON DELETE CASCADE,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  certification_type text NOT NULL CHECK (certification_type IN ('individual','company')),
  provider_id text NOT NULL DEFAULT 'manual',
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','processing','failed')),
  payload_encrypted bytea NOT NULL,
  payload_aad text NOT NULL,
  provider_subject text NOT NULL DEFAULT '',
  submitted_at timestamptz NOT NULL DEFAULT now(),
  reviewed_at timestamptz,
  reviewed_by bigint REFERENCES users(id),
  review_note text NOT NULL DEFAULT ''
);
CREATE INDEX certification_submissions_review ON certification_submissions(status,submitted_at DESC);
CREATE INDEX certification_submissions_user ON certification_submissions(user_id,submitted_at DESC);

CREATE TABLE certification_provider_results (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  submission_id bigint NOT NULL REFERENCES certification_submissions(id) ON DELETE CASCADE,
  provider_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('processing','approved','rejected','failed')),
  provider_subject text NOT NULL DEFAULT '',
  result_encrypted bytea NOT NULL DEFAULT ''::bytea,
  result_aad text NOT NULL DEFAULT '',
  error_message text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX certification_provider_results_submission ON certification_provider_results(submission_id,created_at DESC);

CREATE TABLE certification_reviews (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  submission_id bigint NOT NULL REFERENCES certification_submissions(id) ON DELETE CASCADE,
  profile_id bigint NOT NULL REFERENCES certification_profiles(id) ON DELETE CASCADE,
  reviewer_id bigint NOT NULL REFERENCES users(id),
  decision text NOT NULL CHECK (decision IN ('approved','rejected')),
  note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX certification_reviews_submission ON certification_reviews(submission_id,created_at DESC);

CREATE TABLE certification_settings (
  id smallint PRIMARY KEY CHECK (id=1),
  enabled boolean NOT NULL DEFAULT true,
  require_for_purchase boolean NOT NULL DEFAULT false,
  default_provider_id text NOT NULL DEFAULT 'manual',
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO certification_settings(id) VALUES(1);
