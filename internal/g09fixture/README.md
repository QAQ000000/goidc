# G09 fixture infrastructure (partial implementation)

This package is acceptance infrastructure. Production binaries do not import it.
The helpers do not start a database, seed accounts, execute a worker, or claim capacity.
The explicitly opted-in PG observer test creates and removes a random authorized
PG18 test schema and seeds bounded fixture-only queue rows; this is separate
from protocol-only default tests.
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
before SMTP `250`/HTTP `200`; initialization also fsyncs the parent directory.
Evidence contains no raw destination, body or token. SMTP hashes the plain envelope
address without angle brackets so it can be correlated with the source identity.
SMTP rejects messages exceeding 1 MiB, command lines exceeding its 4 KiB buffer,
more than ten recipients, more than 128 simultaneous connections, and connections exceeding 30 seconds. HTTP request
bodies are limited to 1 MiB; its eventual server must also set bounded server
timeouts. `SMTP.Close()` closes and joins owned connections. Close listeners
before closing the journal.

Acceptance is not proof of recipient delivery or exactly-once behavior. SMTP DATA
is recorded after dot unescaping/newline canonicalization; its hash need not equal
the original wire-byte hash. A lost protocol response can cause a legitimate retry
and a second acceptance. `Snapshot` returns a detached, in-memory record copy;
the private journal remains the durable record. At most 8192 acceptances are stored.
A journal persistence or budget failure remains latched, returns failure to all
later sends, and must invalidate the fixture run (check `Journal.Err()`). These
protocol hashes alone do not establish ticket/outbox IDs or the 600-task cohort;
the runner still needs an explicit source-to-evidence mapping.

## Private control building blocks

`ValidateRequest(raw, runID, lastSequence)` accepts exactly the next sequence and
the actions specified in the design contract: `prepare`, `start-workers`,
`stop-workers`, `snapshot`, `seal-cohorts`, `verify-drain`, `finish`, `abort`.
Only worker actions take slot `0` or `1`. Unknown/duplicate fields, trailing JSON,
unknown actions, replayed/overflowed sequences, case-variant keys and arbitrary SQL/shell/PID/path/env fields
are rejected. This is validation only: it does **not execute any action**.
After validation the runner must consume the sequence even if execution fails.

`WritePrivateJSON` creates the first file exclusively; `ReplacePrivateJSON`
requires an owned, regular, `0600` existing target and uses a same-directory
private temporary file plus atomic rename. Both sync data and the directory.
The directory and its ancestors cannot be symlinks. The future runner owns the
private directory and must serialize control request/response handling; these
helpers are not an interprocess lock or a sandbox against a hostile same-UID
process. No control HTTP endpoint is introduced.

Default focused validation: `go test -race ./internal/g09fixture -count=1`.
With no explicit PG opt-in, protocol tests use only
ephemeral loopback listeners and temporary files; no PostgreSQL, external
channels, release build or production process is involved.
The provider compatibility test resolves the actual compiled SMTP/SMS adapters
and checks their durable sink records, without claiming an outbox or worker run.

## Supplier and read-only PostgreSQL observation

`NewSupplierSink` freezes bounded per-service identities and 32-character
downstream tokens. The real compiled ZJMF adapter uses loopback HTTP; accepted
protocol facts are durable before success. Duplicate raw settle/pay acceptance
does not prove exactly-once charging. See the supplier sink acceptance document
under `docs/g09-supplier-sink-increment-20261017.md`.

`NewPGObserver` validates an explicitly authorized `_test` database, random schema
and supplier IDs without connecting; `Sample` uses bounded read-only PG18
transactions, verifies physical connection identity and latches any error or
concurrent attempt. Queue/entity facts are schema scoped; database statistics
include all its schemas and WAL is cluster wide. No SQL text or message body is
collected. The opt-in `ZJMFGO_G09_PG_OBSERVER_SMOKE=1` test requires an authorized
`ZJMFGO_TEST_DATABASE_URL`; see `docs/g09-pg-observer-increment-20261017.md`.
No full candidate controller, 600-task run or capacity claim is provided here.

## Offline capacity protocol

`CapacityFixture` is a separate, explicit protocol state machine for the
future controller. It accepts the existing 450 source rows in bounded chunks,
seals them through `SealCohortSources` (300 purchases plus 150 tickets, which
map to 600 queue entities), and then checks a caller-supplied read-only
projection one entity at a time. A pending projection remains `draining`;
only six hundred valid terminal observations move the protocol to
`CapacityVerified`. This state is evidence about the supplied projection
only, never a production worker, database, delivery, or capacity result.

`Cleanup` is an ownership/state guard and has no deletion or process side
effect. `ValidateCapacityRequest` and `Apply` use a private sequence and a
separate action set (`register-sources`, `seal-sources`, `begin-drain`,
`verify-drain`, `abort`, `cleanup`); the existing bootstrap control protocol
is unchanged. The focused contract tests require
`ZJMFGO_G09_CAPACITY_FIXTURE_PROTOCOL=1` and use no PostgreSQL or external
service.
