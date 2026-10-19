-- Execution-loop progress is separate from scheduler task audit and API readiness.
-- No business queue is recovered or changed by these records.
CREATE TABLE worker_runtime_instances (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 stopped_at timestamptz
);
CREATE TABLE worker_runtime_lanes (
 instance_id text NOT NULL REFERENCES worker_runtime_instances(id) ON DELETE CASCADE,
 lane text NOT NULL CHECK(lane IN ('provision','payment','file_cleanup','notification_schedule','auto_renew','notification_center','certification')),
 state text NOT NULL DEFAULT 'idle' CHECK(state IN ('idle','running','stopped')),
 poll_interval_seconds integer NOT NULL CHECK(poll_interval_seconds BETWEEN 1 AND 3600),
 stall_after_seconds integer NOT NULL CHECK(stall_after_seconds BETWEEN 30 AND 86400),
 last_poll_at timestamptz,
 started_at timestamptz,
 completed_at timestamptz,
 progress_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 completed_passes bigint NOT NULL DEFAULT 0 CHECK(completed_passes>=0),
 PRIMARY KEY(instance_id,lane)
);
CREATE INDEX worker_runtime_instances_started ON worker_runtime_instances(started_at);
