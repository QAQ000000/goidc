CREATE TABLE scheduler_task_settings (
 id smallint PRIMARY KEY CHECK (id=1),
 version bigint NOT NULL DEFAULT 1,
 enabled boolean NOT NULL DEFAULT true,
 settings jsonb NOT NULL DEFAULT '{"expiry_days":[7,1],"billing_grace_days":0}'::jsonb CHECK(jsonb_typeof(settings)='object'),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO scheduler_task_settings(id) VALUES(1);
