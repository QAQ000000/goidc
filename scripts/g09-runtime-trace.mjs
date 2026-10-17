// Post-stop, private evidence check. Run only after owned namespace exit against an independently verified manifest.
// Does not discover processes or weaken the
// separate file/network audit. Never return trace text or raw argument values.
import { open, realpath } from 'node:fs/promises';
import { constants } from 'node:fs';
import { createHash } from 'node:crypto';
import path from 'node:path';
const fail = () => new Error('g09_resident_trace_invalid');
const requireThat = ok => { if (!ok) throw fail(); };
const names = ['api', 'web', 'worker_0', 'worker_1'];
const sha = value => createHash('sha256').update(value).digest('hex');
const relative = value => typeof value === 'string' && value.length > 0 && !value.includes('\\') && !/[\x00-\x1f\x7f]/.test(value) && !path.posix.isAbsolute(value) && value.split('/').every(x => x && x !== '.' && x !== '..');

// strace strings are C byte strings, not JSON (e.g. UTF-8 octal escapes).
// Reject abbreviations, unknown escapes and malformed UTF-8 rather than infer.
function parseExec(raw) {
  let at = 0;
  const take = value => { requireThat(raw.startsWith(value, at)); at += value.length; };
  const string = () => {
    take('"'); const bytes = [];
    while (at < raw.length && raw[at] !== '"') {
      let c = raw[at++];
      if (c !== '\\') {
        requireThat(c.charCodeAt(0) >= 32 && c.charCodeAt(0) <= 126);
        bytes.push(c.charCodeAt(0)); continue;
      }
      c = raw[at++];
      const escapes = { n: 10, r: 13, t: 9, v: 11, f: 12, b: 8, a: 7, '\\': 92, '"': 34 };
      if (Object.hasOwn(escapes, c)) bytes.push(escapes[c]);
      else {
        requireThat(/[0-7]/.test(c ?? '') && c !== undefined);
        let octal = c;
        while (octal.length < 3 && /[0-7]/.test(raw[at] ?? '') && at < raw.length) octal += raw[at++];
        const byte = Number.parseInt(octal, 8); requireThat(byte <= 255); bytes.push(byte);
      }
    }
    take('"');
    const value = new TextDecoder('utf-8', { fatal: true }).decode(Uint8Array.from(bytes));
    requireThat(!value.includes('\0')); return value;
  };
  take('execve('); const executable = string(); take(', [');
  const argv = [];
  if (!raw.startsWith(']', at)) {
    for (;;) { requireThat(argv.length < 257); argv.push(string()); if (!raw.startsWith(', ', at)) break; take(', '); }
  }
  take('], ');
  requireThat(/^0x[a-f0-9]+ \/\* [0-9]+ vars? \*\/\)\s+= 0$/.test(raw.slice(at)));
  return { executable, argv };
}
async function traceFile(filename) {
  const file = await open(filename, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const before = await file.stat();
    requireThat(before.isFile() && before.nlink === 1 && before.size > 0 && before.size <= 16 * 1024 * 1024);
    const buffer = Buffer.alloc(before.size + 1); let used = 0;
    while (used < buffer.length) { const r = await file.read(buffer, used, buffer.length - used, used); if (!r.bytesRead) break; used += r.bytesRead; }
    const after = await file.stat();
    requireThat(used === before.size && after.size === before.size && after.mtimeMs === before.mtimeMs && after.ctimeMs === before.ctimeMs);
    const bytes = buffer.subarray(0, used);
    const text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
    requireThat(text.endsWith('\n'));
    return { text, digest: sha(bytes) };
  } finally { await file.close(); }
}
export async function verifyG09ResidentTraces({ traceRoot, identities, manifest, description } = {}) {
  try {
    requireThat(typeof traceRoot === 'string' && path.isAbsolute(traceRoot) && path.normalize(traceRoot) === traceRoot && await realpath(traceRoot) === traceRoot);
    requireThat(Array.isArray(identities) && identities.length === names.length && new Set(identities.map(x => x?.name)).size === names.length && identities.every(x => names.includes(x?.name)));
    requireThat(new Set(identities.map(x => x.pid)).size === names.length);
    requireThat(Array.isArray(description?.steps) && Array.isArray(manifest?.artifacts));
    const rows = [];
    for (const name of names) {
      const identity = identities.find(x => x.name === name), stepName = name.startsWith('worker_') ? 'worker' : name;
      requireThat(Number.isSafeInteger(identity.pid) && identity.pid >= 2 && identity.pid <= 4194304);
      const steps = description.steps.filter(x => x.name === stepName); requireThat(steps.length === 1);
      const step = steps[0], isWeb = name === 'web';
      const executable = isWeb ? manifest.runtime?.node?.path : manifest.binaries?.[stepName]?.path;
      const cwd = isWeb ? manifest.web?.appRoot : 'artifact';
      requireThat(relative(executable) && relative(cwd) && (!isWeb || (relative(manifest.web?.entry) && manifest.web.entry.startsWith(`${cwd}/`))));
      const args = isWeb ? [path.posix.relative(cwd, manifest.web.entry)] : [];
      requireThat(step.executable === executable && step.cwd === cwd && JSON.stringify(step.args ?? []) === JSON.stringify(args));
      const artifacts = manifest.artifacts.filter(x => x.path === executable);
      requireThat(artifacts.length === 1 && !artifacts[0].link && /^[a-f0-9]{64}$/.test(artifacts[0].sha256));
      const expectedExe = `/release/${executable}`;
      requireThat(identity.executable === expectedExe && identity.cwd === `/release/${cwd}` && identity.executableSHA256 === artifacts[0].sha256);
      const filename = `${name}.trace.${identity.pid}`;
      const trace = await traceFile(path.join(traceRoot, filename));
      const execs = trace.text.split('\n').filter(line => /\bexecve(?:at)?\(/.test(line));
      requireThat(execs.length === 1);
      const parsed = parseExec(execs[0]);
      requireThat(parsed.executable === expectedExe && JSON.stringify(parsed.argv) === JSON.stringify([expectedExe, ...args]));
      rows.push({ name, pid: identity.pid, traceFile: filename, traceSHA256: trace.digest, executableSHA256: artifacts[0].sha256, executable: expectedExe, argumentCount: args.length });
    }
    return { coverage: 'same-pid-successful-execve', processes: rows };
  } catch { throw fail(); }
}
