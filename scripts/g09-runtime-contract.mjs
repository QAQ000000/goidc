import { link, lstat, mkdir, open, readFile, rm } from 'node:fs/promises';
import path from 'node:path';
import { randomBytes } from 'node:crypto';
import { noLinks, safeRelative } from './release-candidate-lib.mjs';

const requireThat = (ok, code) => { if (!ok) throw new Error(code); };
const integer = (n, min, max) => Number.isSafeInteger(n) && n >= min && n <= max;
export const g09RuntimeFiles = Object.freeze([
  'scripts/accept-g09-runtime.mjs', 'scripts/g09-runtime-contract.mjs',
  'scripts/g09-runtime-process.mjs', 'scripts/g09-process-sampler.mjs',
  'scripts/g09-runtime-recovery.mjs', 'scripts/g09-runtime-trace.mjs',
  'scripts/accept-release-runtime.mjs', 'scripts/release-runtime-lib.mjs',
  'scripts/release-runtime-acceptance-lib.mjs', 'scripts/release-runtime-builder-lib.mjs',
  'scripts/release-runtime-trace-selection.mjs', 'scripts/release-candidate-lib.mjs',
]);

// Deliberately separate from release_runtime_* acceptance. Never relax its
// validator to admit G09 schemas or libpq identity overrides.
export function validateG09RuntimeDSN(raw, schema) {
  let url;
  try { url = new URL(raw); } catch { throw new Error('g09_database_invalid'); }
  requireThat(['postgres:', 'postgresql:'].includes(url.protocol) && url.hostname === '127.0.0.1' &&
    /^[0-9]+$/.test(url.port) && integer(Number(url.port), 1025, 65535) &&
    /^\/[A-Za-z0-9_]+_test$/.test(url.pathname) && !url.hash && url.username, 'g09_database_invalid');
  const scoped = schema !== undefined;
  requireThat(!scoped || /^zjmfgo_test_[a-f0-9]{16}$/.test(schema), 'g09_schema_invalid');
  const keys = scoped ? ['sslmode', 'search_path', 'application_name', 'connect_timeout'] : ['sslmode'];
  for (const [key] of url.searchParams) requireThat(keys.includes(key) && url.searchParams.getAll(key).length === 1, 'g09_database_override');
  requireThat(['disable', 'prefer'].includes(url.searchParams.get('sslmode') ?? 'disable'), 'g09_database_tls_invalid');
  if (scoped) requireThat(url.searchParams.get('search_path') === schema &&
    url.searchParams.get('application_name') === `g09_candidate_${schema}` &&
    url.searchParams.get('connect_timeout') === '5', 'g09_database_scope_invalid');
  return Object.freeze({ port: Number(url.port), database: url.pathname.slice(1) });
}

export function g09MigrationInputs(manifest) {
  requireThat(Array.isArray(manifest?.migrations) && manifest.migrations.length >= 1 && manifest.migrations.length <= 1000, 'g09_migrations_invalid');
  const seen = new Set();
  const rows = manifest.migrations.map(row => {
    requireThat(/^db\/migrations\/[0-9][A-Za-z0-9_]*\.sql$/.test(row?.path ?? '') && /^[a-f0-9]{64}$/.test(row?.sha256 ?? '') && !seen.has(row.path), 'g09_migrations_invalid');
    seen.add(row.path);
    return { version: path.posix.basename(row.path), checksum: row.sha256 };
  }).sort((a, b) => a.version < b.version ? -1 : a.version > b.version ? 1 : 0);
  return rows;
}

export function validateG09Command(command, runId, sequence) {
  requireThat(/^g09_candidate_[a-f0-9]{24}$/.test(runId ?? '') && integer(sequence, 1, 32), 'g09_control_identity_invalid');
  requireThat(command && typeof command === 'object' && !Array.isArray(command) &&
    Object.keys(command).sort().join(',') === 'action,runId,sequence' && command.runId === runId && command.sequence === sequence &&
    ['ready', 'verify-migrations', 'snapshot', 'finish', 'abort'].includes(command.action), 'g09_control_command_invalid');
  return Object.freeze({ ...command });
}

// One writer per directory; complete/fsynced files become visible atomically.
// This is for owned control files, never an arbitrary product write endpoint.
export async function writeG09ControlJSON(filename, value, { uid = process.getuid(), gid = process.getgid() } = {}) {
  const directory = path.dirname(filename);
  await noLinks(directory);
  const parent = await lstat(directory);
  requireThat(parent.isDirectory() && parent.uid === uid && (process.getuid() === 0 || uid === process.getuid()) && (parent.mode & 0o022) === 0, 'g09_control_directory_invalid');
  requireThat(!(await lstat(filename).catch(error => { if (error.code === 'ENOENT') return null; throw error; })), 'g09_control_exists');
  const temporary = `${filename}.${process.pid}.${randomBytes(8).toString('hex')}.tmp`;
  let file;
  try {
    file = await open(temporary, 'wx', 0o600);
    await file.chown(uid, gid);
    await file.writeFile(JSON.stringify(value) + '\n'); await file.sync(); await file.close(); file = null;
    await link(temporary, filename);
    await rm(temporary);
    const dir = await open(directory, 'r'); try { await dir.sync(); } finally { await dir.close(); }
  } catch { throw new Error('g09_control_write_failed'); }
  finally { await file?.close().catch(() => {}); await rm(temporary, { force: true }); }
}

export async function readG09ControlJSON(filename, { uid = process.getuid() } = {}) {
  try {
    await noLinks(filename);
    const info = await lstat(filename);
    requireThat(info.isFile() && info.uid === uid && (info.mode & 0o777) === 0o600 && info.size <= 1048576, 'g09_control_file_invalid');
    return JSON.parse(await readFile(filename, 'utf8'));
  } catch { throw new Error('g09_control_read_failed'); }
}

// The sealed tree omits empty directories. Create the writable-cache mount
// target only in the independent run copy before that tree becomes read-only.
export async function prepareG09CacheMountpoint(release, appRoot) {
  safeRelative(appRoot);
  requireThat(appRoot.startsWith('artifact/web/'), 'g09_cache_root_invalid');
  const parent = await noLinks(path.join(release, appRoot, '.next'));
  const target = path.join(parent, 'cache');
  await mkdir(target, { mode: 0o755, recursive: true });
  await noLinks(target);
  requireThat((await lstat(target)).isDirectory(), 'g09_cache_mountpoint_invalid');
  return target;
}
