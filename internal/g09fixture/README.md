# G09 fixture infrastructure (partial implementation)

This package is acceptance infrastructure. Production binaries do not import it.
It does not start a database, seed accounts, execute a worker, or claim capacity.
The opt-in fixture runner and its candidate/namespace integration remain to be
implemented. In particular, **there is no executable 600-task fixture yet**.

## Protocol sinks

Create an owned, absolute, non-symlink `0700` directory. `NewJournal` exclusively
creates a `0600` JSONL file. `StartSMTP(journal)` binds an ephemeral IPv4 loopback
port; configure SMTP with its address, `tls_mode=none`, and no username/password.
Only `.test` recipients are accepted. `SMSHandler(journal, token)` implements the
production generic HTTP JSON request `{recipient,message,kind}`. The caller must
serve it on an IPv4 loopback listener. Synthetic SMS destinations must use the
fixture-only `+999` prefix (10–16 characters including `+`); no external sending
occurs. These unassigned synthetic numbers are not real phone subscribers.

Each accepted SMTP recipient or SMS request appends a sequence, UTC acceptance
time, channel, SHA-256 recipient hash and message hash. The journal is fsynced
before SMTP `250`/HTTP `200`. Evidence contains no raw destination, body or token.
SMTP rejects messages exceeding 1 MiB, command lines exceeding its 4 KiB buffer,
more than ten recipients, and connections exceeding 30 seconds. HTTP request
bodies are limited to 1 MiB; its eventual server must also set bounded server
timeouts. `SMTP.Close()` closes and joins owned connections. Close listeners
before closing the journal.

Acceptance is not proof of recipient delivery or exactly-once behavior. SMTP DATA
is recorded after dot unescaping/newline canonicalization; its hash need not equal
the original wire-byte hash. A lost protocol response can cause a legitimate retry
and a second acceptance. `Snapshot` returns a detached, in-memory record copy;
the private journal remains the durable record. A journal failure returns failure
to the worker and must invalidate the fixture run.

## Private control building blocks

`ValidateRequest(raw, runID, lastSequence)` accepts exactly the next sequence and
the actions specified in the design contract: `prepare`, `start-workers`,
`stop-workers`, `snapshot`, `seal-cohorts`, `verify-drain`, `finish`, `abort`.
Only worker actions take slot `0` or `1`. Unknown/duplicate fields, trailing JSON,
unknown actions, replayed sequences and arbitrary SQL/shell/PID/path/env fields
are rejected. This is validation only: it does **not execute any action**.
After validation the runner must consume the sequence even if execution fails.

`WritePrivateJSON` creates the first file exclusively; `ReplacePrivateJSON`
requires an owned, regular, `0600` existing target and uses a same-directory
private temporary file plus atomic rename. Both sync data and the directory.
The directory and its ancestors cannot be symlinks. The future runner owns the
private directory and must serialize control request/response handling; these
helpers are not an interprocess lock or a sandbox against a hostile same-UID
process. No control HTTP endpoint is introduced.

Focused validation: `go test -race ./internal/g09fixture -count=1`. Tests use only
ephemeral loopback listeners and temporary files; no PostgreSQL, external
channels, release build or production process is involved.
