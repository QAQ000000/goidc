import { readFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

// Read-only: never updates the manifest, historical hashes, or screenshot files.
export async function checkEvidence({ manifest, root = process.cwd() }) {
  if (!Array.isArray(manifest.pages)) throw new Error('browser evidence pages must be an array');
  const summary = { total: manifest.pages.length, verified: 0, mismatch: 0, missing_hash: 0, missing_file: 0, invalid_hash: 0, read_error: 0, declared_mismatch: 0, unexpected_mismatch: 0, strict_failures: 0, unacknowledged_failures: 0 };
  const pages = [];
  for (const [index, entry] of manifest.pages.entries()) {
    const result = { index, newRoute: entry.newRoute, width: entry.width, screenshot: entry.screenshot ?? null, expectedSha256: entry.screenshotSha256 ?? null, actualSha256: null, status: null, declaredMismatch: false, usableForIntegrityProof: false };
    if (typeof entry.screenshot !== 'string' || !entry.screenshot.trim()) {
      result.status = 'missing_file';
    } else {
      try {
        result.actualSha256 = createHash('sha256').update(await readFile(path.resolve(root, entry.screenshot))).digest('hex');
        if (!entry.screenshotSha256) result.status = 'missing_hash';
        else if (typeof entry.screenshotSha256 !== 'string' || !/^[a-f0-9]{64}$/i.test(entry.screenshotSha256)) result.status = 'invalid_hash';
        else result.status = result.actualSha256 === entry.screenshotSha256.toLowerCase() ? 'verified' : 'mismatch';
      } catch (error) {
        result.status = error.code === 'ENOENT' ? 'missing_file' : 'read_error';
        result.error = error.code ?? error.message;
      }
    }
    if (result.status === 'mismatch') {
      const declaration = entry.screenshotIntegrity;
      // Bind acknowledgement to both hashes: later overwrites or edits need a fresh review.
      result.declaredMismatch = declaration?.status === 'mismatch'
        && declaration.expectedSha256 === entry.screenshotSha256
        && declaration.observedSha256 === result.actualSha256
        && declaration.usableForIntegrityProof === false
        && typeof declaration.checkedAt === 'string' && !!declaration.checkedAt.trim()
        && typeof declaration.reason === 'string' && !!declaration.reason.trim();
      summary[result.declaredMismatch ? 'declared_mismatch' : 'unexpected_mismatch']++;
    }
    result.usableForIntegrityProof = result.status === 'verified';
    summary[result.status]++;
    if (['missing_file', 'invalid_hash', 'read_error'].includes(result.status) || (result.status === 'mismatch' && !result.declaredMismatch)) summary.unacknowledged_failures++;
    if (result.status !== 'verified') summary.strict_failures++;
    pages.push(result);
  }
  return { summary, strictPassed: summary.strict_failures === 0, overallIncomplete: summary.strict_failures > 0, recordingConsistent: summary.unacknowledged_failures === 0, note: 'verified only proves current bytes match the recorded digest; historical PASS/checks are not rerun. Declared mismatches remain unusable; missing_hash means integrity was not established.', pages };
}

async function main(args) {
  let manifestPath = 'docs/admin-restore-browser-evidence.json';
  let root = process.cwd();
  let strict = false;
  let strictUnacknowledged = false;
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--strict') strict = true;
    else if (args[i] === '--strict-unacknowledged') strictUnacknowledged = true;
    else if (['--root', '--manifest'].includes(args[i]) && args[i + 1] && !args[i + 1].startsWith('--')) {
      if (args[i] === '--root') root = path.resolve(args[++i]);
      else manifestPath = args[++i];
    } else throw new Error('Usage: node scripts/check-admin-browser-evidence.mjs [--manifest PATH] [--root SCREENSHOT_ROOT] [--strict | --strict-unacknowledged]');
  }
  if (strict && strictUnacknowledged) throw new Error('Choose either --strict or --strict-unacknowledged');
  const manifest = JSON.parse(await readFile(manifestPath, 'utf8'));
  const report = await checkEvidence({ manifest, root });
  process.stdout.write(JSON.stringify({ manifest: manifestPath, root, ...report }, null, 2) + '\n');
  if ((strict && !report.strictPassed) || (strictUnacknowledged && !report.recordingConsistent)) process.exitCode = 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  main(process.argv.slice(2)).catch(error => {
    process.stderr.write(error.message + '\n');
    process.exitCode = 1;
  });
}
