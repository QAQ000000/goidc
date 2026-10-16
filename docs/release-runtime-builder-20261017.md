# G04 v2 runtime builder implementation preparation (2026-10-17)

Status: implementation and fixture verification only. No complete Next build, runtime candidate, database operation, service installation, deployment, or same-package runtime acceptance has been executed in this batch. G04/G09 remain open. The next real candidate must freeze a new clean commit containing both builder and verifier, plus the final application changes; `9cce4f5` is this implementation branch's starting point, not a release candidate.

`build-release-runtime-candidate.mjs` is a separate opt-in v2 builder. Existing v1 CLI, manifest and protection rules retain their defaults. `web/next.config.ts` enables standalone only when the isolated builder sets `ZJMFGO_RUNTIME_BUILD=1`; its tracing root is the fixed checkout root. The reviewed installed Next 16.3.8 `output` and `distDir` documentation and `copyTracedFiles` implementation require this root to contain the actual dependency path, explicit webpack, `.next/static`/public supplementation, and the actual generated app root rather than a guessed root `server.js`.

The default command performs preflight only; `--execute` as the final flag authorizes compilation within the already specified isolated candidate output. There is no installation/download fallback. Supply all arguments explicitly:

```sh
node scripts/build-release-runtime-candidate.mjs \
  --source /absolute/clean/checkout --commit FULL_COMMIT \
  --output /tmp/NEW-EXCLUSIVE-CANDIDATE \
  --dependencies /absolute/REAL/TRUSTED/node_modules \
  --dependency-provenance /controlled/dependencies.json \
  --dependency-provenance-sha256 EXTERNAL_SHA256 \
  --node /absolute/TRUSTED/node --node-license /absolute/TRUSTED/LICENSE \
  --node-provenance /controlled/node.json \
  --node-provenance-sha256 EXTERNAL_SHA256 \
  --budget /controlled/budget.json --budget-sha256 EXTERNAL_SHA256 \
  --api-internal-url http://127.0.0.1:58088 \
  --web-origin http://127.0.0.1:53088 --runtime-user www
```

A caller-provided SHA authenticates only against the caller's independent trusted record. Creating a JSON file that describes an unknown shared install does not establish trusted provenance. Do not fabricate a successful `npm ci`/package-integrity history. Provenance records must contain no credentials, DSNs, private registry tokens or other secret values. All external JSON inputs reject symlinked paths and require separate trusted SHA256 values. The returned `manifestSHA256` must also be stored independently of the sealed directory; the builder deliberately does not create a self-authenticating in-directory anchor.

Dependency provenance has the following shape; `files` is the complete sorted input inventory from the existing inventory helper, including restricted internal link records. Each selected runtime file must ultimately map to an ordinary exact input file; trace directory/link entries are rejected rather than recursively expanded.

```json
{
  "format": "zjmfgo-runtime-dependency-provenance-v1",
  "lockSHA256": "sha256-of-selected-web-package-lock",
  "inputTreeSHA256": "sha256-of-JSON.stringify(files)",
  "installation": {
    "command": "npm ci",
    "source": "independently trusted installation record",
    "registry": "https://registry.npmjs.org",
    "integrityVerified": true,
    "nodeVersion": "v24.16.0",
    "npmVersion": "installation npm version",
    "platform": "linux/x64",
    "immutability": "documented input ownership/control, plus private readonly build mount"
  },
  "files": []
}
```

Node provenance is `{format:'zjmfgo-runtime-node-provenance-v1',version,platform:'linux/x64',source,sha256,licenseSHA256}`. Only that exact executable and license are bundled; npm is not installed or used at runtime. Build Go commands use existing trusted local toolchain, `GOTOOLCHAIN=local`, `GOPROXY=off`, `GOSUMDB=off`, readonly module mode, and a private build cache. Missing modules fail instead of downloading. The existing Go module cache is a local build input mounted read-only within the private namespace, not a runtime dependency. `readelf` inventories the actual loader/NEEDED entries of Go, Node and selected native `.node` files without executing package ELF files. Target-host compatibility still requires acceptance against this declared baseline.

Budget provenance is externally pinned, with strictly positive integer values:

```json
{
  "format": "zjmfgo-runtime-budget-v1",
  "approvedBy": "named budget approval record",
  "maxPeakBytes": 1,
  "minReserveBytes": 1,
  "minReserveInodes": 1,
  "maxBuildSeconds": 1800,
  "components": {
    "checkout": 1,
    "buildTemporary": 1,
    "standalone": 1,
    "sealed": 1,
    "runCopy": 1,
    "testDatabaseAndLogs": 1
  }
}
```

The `1` byte values are placeholders, not usable estimates. The six components must fit the approved peak; preflight requires free bytes for peak plus reserve. Measured tracked checkout and Node sizes must fit their component allowances. A parent process checks disk/inodes and total consumption every 250 ms during each bounded command and kills only its owned process group when a bound is crossed. This sampling cannot promise that a large single write never transiently exceeds the reserve; filesystem quotas would be required for that stronger guarantee. Other host writers can also cause a conservative stop. Never clean other worktrees, candidates, shared caches, databases or running output to satisfy this budget.

Before any compilation the script checks private mount namespace capability. Within that namespace, dependencies are bind-mounted read-only at the checkout's real `web/node_modules` path and mountinfo is checked. Namespace absence is BLOCKED, with no root-wide tracing, external symlink, global mount, full node_modules copy, or independent npm installation fallback. Source, dependency and Node/license inputs are checked before and after the build. The temporary source checkout remains alive while all trace paths are resolved. Every trace input maps to fixed source, trusted dependencies or same-build `.next` files; every selected dependency is trace-backed. Unknown, escaped, missing, directory, linked or changed inputs stop sealing. The actual generated app root and config are bound to same-build routes and launch description. The standalone tree is moved into the candidate; the remaining checkout/build output is deleted only after the namespace exits. Only the invocation's newly created exclusive output is removed on failure.

`launch/description.json` is a declarative contract, not a process supervisor or installed service. It binds package binary names, bundled Node, actual standalone entry/cwd, loopback endpoints, ordinary runtime user, external config version, required keys, mutable-root paths, readiness ordering, double migration, bounded cleanup, and full inventory before/after use. The implementation does not yet perform runtime write auditing; the description explicitly marks that as pending acceptance. It must not be interpreted as proof that Next writes only cache. No process should run directly in sealed output.

The exported `controlledRuntimeEnvironment` helper validates an independently owned ordinary config file with exact `0600`, expected UID, bounded JSON, version and key allowlist. Required values are `DATABASE_URL` and three 32-byte hex encryption keys. Its output is built from an empty environment, fixed loopback/Cookie/origin settings and explicitly allowed keys. It never inherits `PATH`, `HOME`, Node module options, provider, payment or notification credentials. It returns sensitive values solely for a future caller's process environment; never serialize/log the returned object. The config helper does not prove a dedicated test database, isolated schema, network egress restrictions, port availability, host libraries, process identity or readiness; those belong to the opt-in runtime acceptance controller and are required before launching anything.

Validation in this implementation batch uses small fixtures for provenance, source/trace mappings, directory/link escapes, digest tampering, unknown dependencies, secret config permissions/version/keys, explicit ports, controlled environment construction, disk refusal, and bounded process termination. Existing v1 tests also run. Full standalone compilation, real native loading, relocated runtime-copy launch, PG18/migrate/worker/page-chunk/API tests and host write/egress auditing remain **未执行**. The input installation's trusted provenance and an approved real peak budget must be supplied before central build acceptance.
