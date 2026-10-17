# G09 direct-child lifecycle probe — 2026-10-17

Preparatory lifecycle verification only. This is not a sealed candidate acceptance,
API/Web/worker business test, PG test, or capacity PASS. No resident services were
signaled or used, and no candidate was built.

`launchG09TracedChild({executable,args=[],cwd,env,tracePrefix,stopSeconds=5})`
is an asynchronous private controller helper in `scripts/g09-runtime-process.mjs`.
It returns frozen `{pid,tracerPID,tracePrefix,identity,done,stop}`. The executable
must be a canonical absolute regular-file path; `cwd` must be canonical. Trace
parent directory must be canonical, owned by the effective ordinary controller
UID, inaccessible to other users, and have no preexisting files matching the
trace prefix. It refuses prefix collisions rather than truncating evidence.
The controller must independently verify executable hash, establish the required
UID/mount/network/PID isolation, and check application service readiness.

`strace -DDD -ff` leaves the actual application at the direct spawned child PID,
while moving the tracer into a separate session and process group. Readiness
here means executable/PPID/group/tracer identity and strict sampler registration,
not service readiness. Per-PID file/network filters, raw network syscall arguments,
and trace length match the existing runtime audit. The tracer PID comes only from
that owned child's `TracerPid`, with group/session verified. There is no global
process discovery or signaling; only the explicitly spawned application group
and captured tracer group are eligible for signaling after fresh identity checks. Arguments, environment and driver error
text are not included in public helper errors. Trace paths and handle identities
are private controller information; do not publish the raw traces or handle.

`identity` is the existing strict sampler registration, with name `candidate`.
For multiple applications, pass `{...handle.identity,name:'api'}` (or another
valid unique name) to the existing sampler; this changes the logical label only.
The sampler still checks exact executable, direct PPID, UID, namespace and start
time; application grandchildren are not covered by that registration.
`done` resolves direct application exit `{code,signal,failed}`. It alone does not
prove tracer cleanup. Always await idempotent `stop()`, even after natural exit.
Stop sends TERM to the owned application group, waits the configured 1–30 seconds,
and checks group disappearance and direct-child reap. A deadline triggers KILL
and rejects `runtime_term_deadline` even after successful forced cleanup. The
captured tracer is checked against its recorded start time, UID, executable,
PGID and SID before each TERM or KILL, with
fixed bounds if still live. Missing or zombie tracer means no running tracer;
namespace init owns orphan reaping. A daemonized tracer cannot be reaped by the
controller via Node's direct-child `done` promise.

Very short commands can exit before identity registration and are refused as
`runtime_launch_failed`; this helper is for resident candidate processes, not
migration commands. A candidate must not daemonize or escape its owned group.
The outer namespace lifetime remains the final containment boundary for startup
failures before tracer identity capture, controller termination, and hostile
session/group changes. This helper is not itself a sandbox.

Verification:

* `node --test scripts/g09-runtime-process.test.mjs`: refusal guards run by default;
  runtime subprocess tests are opt-in with `G09_RUNTIME_PROCESS_TEST=1`.
* Host opt-in probe: all 7 tests passed, including actual strace successful exec,
  strict sampler, TERM, natural exit, forced KILL with deadline rejection,
  failed exec, and trace collision preservation.
* An eighth containment guard test also passed: an application exits while its
  sleep descendant remains in the original group. Stop refuses the stale group
  (`runtime_ownership_lost`), proves the descendant remains live, and still stops
  the independently anchored tracer. The descendant expires naturally; the
  disposable outer PID namespace is the final containment owner.
* All 8 tests passed as UID/GID 65534, no capabilities, with private PID,
  mount and network namespaces. Invocation (runtime path is host-specific):

```sh
bwrap --unshare-all --die-with-parent \
  --ro-bind /usr /usr --ro-bind /lib /lib --ro-bind /lib64 /lib64 \
  --ro-bind /www/server/nodejs/v24.16.0/bin/node /node \
  --ro-bind /etc/hostname /etc/hostname \
  --ro-bind /tmp/zjmfgo-g09-runtime-process/scripts /control \
  --proc /proc --dev /dev --tmpfs /tmp \
  --uid 65534 --gid 65534 --cap-drop ALL --chdir /control \
  --setenv G09_RUNTIME_PROCESS_TEST 1 --setenv G09_RUNTIME_CONTAINMENT_TEST 1 \
  /node --test /control/g09-runtime-process.test.mjs
```

Only disposable `/tmp/g09-*` fixtures and private namespace storage were used;
no PG, external network, application data or shared API/worker/Web process was
accessed. The failed-exec test deliberately attempts the nonexecutable regular
file `/etc/hostname`; raw driver stderr is discarded.

## Ownership follow-up

The application group anchor is recorded immediately after spawn, before sampler
registration. Every application signal rechecks PID start ticks, direct PPID,
PGID/SID and all UID fields; every tracer signal rechecks start ticks, PGID/SID,
all UID fields and `/usr/bin/strace` executable. The group signal is refused if
its live leader cannot prove ownership. A vanished leader plus a still-existing
group is **not** accepted as owned, even when descendants are likely ours. An
absent group requires no signal. A replaced PID or unknown identity yields
`runtime_ownership_lost`; no stale group is signaled and no complete cleanup is
claimed. The helper still attempts independently verified tracer cleanup after
an application ownership refusal. The outer disposable namespace must then end.

These are immediate `/proc` identity checks before each group signal, not an
atomic kernel pidfd/group operation; namespace isolation and trusted candidates
remain required. PID reuse is not forced in tests. The actual leader-loss probe
covers the meaningful refusal path without signaling unrelated processes.
