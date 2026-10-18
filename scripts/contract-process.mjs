import { spawn } from 'node:child_process';

// Build tools may use their caches and proxy, but application configuration and
// credentials must never be inherited by contract-test subprocesses.
export function contractBuildEnvironment(source = process.env) {
  const keys = ['PATH', 'HOME', 'TMPDIR', 'TMP', 'TEMP', 'GOCACHE', 'GOMODCACHE', 'GOPATH', 'CGO_ENABLED', 'CC', 'CXX', 'PKG_CONFIG', 'SSL_CERT_FILE', 'SSL_CERT_DIR', 'HTTPS_PROXY', 'HTTP_PROXY', 'ALL_PROXY', 'NO_PROXY', 'https_proxy', 'http_proxy', 'all_proxy', 'no_proxy'];
  return { ...Object.fromEntries(keys.filter(key => source[key] !== undefined).map(key => [key, source[key]])), GOENV: 'off', GOTOOLCHAIN: 'local', LANG: 'C', LC_ALL: 'C' };
}

// Each command owns a process group. A deadline, output overflow or interrupted
// gate kills the entire group, including a tracer or Go test's child process.
export async function runContractCommand(command, args, { cwd, env, timeoutMs, maxBytes = 8 * 1024 * 1024, label = 'contract command' }) {
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs < 1 || !Number.isSafeInteger(maxBytes) || maxBytes < 1) throw new Error('Invalid contract command bounds');
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, env, detached: true, stdio: ['ignore', 'pipe', 'pipe'] });
    const stdout = [], stderr = [];
    let bytes = 0, failure;
    const kill = () => {
      if (!child.pid) return;
      try { process.kill(-child.pid, 'SIGKILL'); } catch (error) { if (error.code !== 'ESRCH') failure ??= `${label} cleanup failed`; }
    };
    const fail = message => { failure ??= message; kill(); };
    const interrupt = () => fail(`${label} interrupted`);
    const timer = setTimeout(() => fail(`${label} exceeded its deadline`), timeoutMs);
    process.once('SIGINT', interrupt);
    process.once('SIGTERM', interrupt);
    const capture = target => chunk => {
      bytes += chunk.length;
      if (bytes > maxBytes) fail(`${label} exceeded its output limit`);
      else target.push(chunk);
    };
    child.stdout.on('data', capture(stdout));
    child.stderr.on('data', capture(stderr));
    child.on('error', () => { failure ??= `${label} could not start`; });
    child.on('close', (status, signal) => {
      clearTimeout(timer);
      process.removeListener('SIGINT', interrupt);
      process.removeListener('SIGTERM', interrupt);
      kill();
      const result = { status, signal, stdout: Buffer.concat(stdout).toString('utf8'), stderr: Buffer.concat(stderr).toString('utf8') };
      if (failure) reject(new Error(failure));
      else resolve(result);
    });
  });
}
