-- Existing live PNG challenges retain their hash and remain valid after upgrade.
ALTER TABLE auth_captcha_challenges
 ADD COLUMN provider_id text NOT NULL DEFAULT 'builtin_png' CHECK (length(provider_id) BETWEEN 1 AND 128),
 ADD COLUMN profile_id text NOT NULL DEFAULT 'builtin' CHECK (length(profile_id) BETWEEN 1 AND 128),
 ADD COLUMN profile_fingerprint text NOT NULL DEFAULT '' CHECK (profile_fingerprint='' OR length(profile_fingerprint)=64),
 ADD COLUMN challenge_kind text NOT NULL DEFAULT 'image' CHECK (challenge_kind IN ('image','click','widget')),
 ADD COLUMN provider_state bytea NOT NULL DEFAULT ''::bytea CHECK (octet_length(provider_state)<=16384);
