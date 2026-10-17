import { readdir, readFile, stat } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const DEFAULT_LIMITS = Object.freeze({
  maxActiveBytes: 1_500_000,
  maxFileBytes: 128 * 1024,
});

// Existing artifacts are tolerated only when they are part of the repository
// baseline. New machine data must be moved to output or external storage.
export const ARTIFACT_ALLOWLIST = Object.freeze(new Set());

const DATE_SUFFIX = /-\d{8}\.md$/i;
const ARTIFACT_SUFFIXES = [
  '.7z', '.avif', '.bin', '.csv', '.gz', '.gif', '.jpeg', '.jpg', '.json',
  '.jsonl', '.ndjson', '.pdf', '.png', '.tar', '.tar.gz', '.tgz', '.tsv',
  '.webp', '.zip',
];
const EXCLUDED_ACTIVE_DIRS = new Set(['archive', 'evidence']);

const normalize = filename => filename.split(path.sep).join('/');
const isUnderDocs = filename => filename === 'docs' || filename.startsWith('docs/');
const isMarkdown = filename => filename.toLowerCase().endsWith('.md');
const isArtifactName = filename => ARTIFACT_SUFFIXES.some(suffix => filename.toLowerCase().endsWith(suffix));

async function walk(root, relative = '') {
  const directory = path.join(root, relative);
  let entries;
  try { entries = await readdir(directory, { withFileTypes: true }); }
  catch (error) {
    if (error.code === 'ENOENT') return [];
    throw error;
  }
  const files = [];
  for (const entry of entries) {
    const child = path.join(relative, entry.name);
    if (entry.isDirectory()) files.push(...await walk(root, child));
    else if (entry.isFile() || entry.isSymbolicLink()) files.push(normalize(child));
  }
  return files;
}

function gitFiles(cwd, args) {
  const result = spawnSync('git', ['-C', cwd, ...args], { encoding: 'buffer' });
  if (result.error || result.status !== 0) return null;
  return result.stdout.toString('utf8').split('\0').filter(Boolean).map(normalize);
}

function repositoryFiles(cwd, allFiles, baselineRef) {
  const baseline = gitFiles(cwd, baselineRef
    ? ['ls-tree', '-r', '--name-only', '-z', baselineRef]
    : ['ls-files', '-z']);
  // Read the current docs tree from disk instead of `git ls-files --others` so
  // an ignored JSON/binary cannot bypass the guard before it is committed.
  if (baseline) {
    const baselineSet = new Set(baseline);
    return { baseline: baselineSet, current: new Set(allFiles), git: true };
  }
  return { baseline: new Set(), current: new Set(allFiles), git: false };
}

async function binaryLike(filename, cwd) {
  if (isArtifactName(filename)) return true;
  try {
    const info = await stat(path.join(cwd, filename));
    if (!info.isFile() || info.size === 0) return false;
    const sample = await readFile(path.join(cwd, filename), { encoding: null });
    const bytes = sample.subarray(0, Math.min(sample.length, 8192));
    if (bytes.includes(0)) return true;
    let controls = 0;
    for (const byte of bytes) {
      if (byte < 7 || (byte > 14 && byte < 32)) controls += 1;
    }
    return bytes.length > 0 && controls / bytes.length > 0.30;
  } catch { return false; }
}

function activeMarkdown(files) {
  return files.filter(filename => {
    if (!isUnderDocs(filename) || !isMarkdown(filename)) return false;
    const parts = filename.split('/');
    return !parts.slice(1, -1).some(part => EXCLUDED_ACTIVE_DIRS.has(part));
  });
}

/**
 * Inspect the documentation tree without changing files.
 *
 * `baselineFiles` and `currentFiles` can be supplied by callers/tests. When
 * omitted they are read from Git, so existing historical artifacts are not
 * mistaken for newly introduced pollution.
 */
export async function checkDocs({
  cwd = process.cwd(),
  requireIndex = true,
  maxActiveBytes = DEFAULT_LIMITS.maxActiveBytes,
  maxFileBytes = DEFAULT_LIMITS.maxFileBytes,
  baselineRef,
  baselineFiles: suppliedBaseline,
  currentFiles: suppliedCurrent,
} = {}) {
  const allFiles = await walk(cwd, 'docs');
  const repo = suppliedBaseline && suppliedCurrent
    ? { baseline: new Set([...suppliedBaseline].map(normalize)), current: new Set([...suppliedCurrent].map(normalize)), git: false }
    : repositoryFiles(cwd, allFiles, baselineRef);
  const currentDocs = [...repo.current].filter(isUnderDocs).sort();
  const baseline = repo.baseline;
  const newDocs = currentDocs.filter(filename => !baseline.has(filename));
  const active = activeMarkdown(currentDocs);
  const errors = [];
  const addError = (rule, filename, message) => errors.push({ rule, path: filename, message });

  const indexPath = 'docs/INDEX.md';
  if (requireIndex && !repo.current.has(indexPath)) {
    addError('index', indexPath, 'docs/INDEX.md is the required active documentation entry point');
  }

  const newDateDocs = newDocs.filter(filename => isMarkdown(filename) && DATE_SUFFIX.test(filename));
  for (const filename of newDateDocs) {
    addError('date-suffix', filename, 'new Markdown files under docs/ must use a living-document name, not -YYYYMMDD.md');
  }

  const newArtifacts = [];
  for (const filename of newDocs) {
    if (!isArtifactName(filename) && !(await binaryLike(filename, cwd))) continue;
    newArtifacts.push(filename);
    addError('artifact', filename, 'new JSON/binary artifacts belong in output or external artifact storage, not docs/');
  }

  let activeBytes = 0;
  const oversized = [];
  for (const filename of active) {
    const info = await stat(path.join(cwd, filename));
    activeBytes += info.size;
    if (info.size > maxFileBytes) oversized.push({ path: filename, bytes: info.size, maxBytes: maxFileBytes });
  }
  for (const file of oversized) {
    addError('file-size', file.path, `${file.bytes} bytes exceeds the ${file.maxBytes}-byte active Markdown limit`);
  }
  if (activeBytes > maxActiveBytes) {
    addError('active-size', 'docs/', `${activeBytes} bytes exceeds the ${maxActiveBytes}-byte active Markdown budget`);
  }

  return {
    ok: errors.length === 0,
    root: cwd,
    git: repo.git,
    baselineRef: baselineRef ?? 'HEAD/worktree',
    limits: { maxActiveBytes, maxFileBytes },
    baselineArtifacts: currentDocs.filter(filename => baseline.has(filename) && isArtifactName(filename)),
    newFiles: newDocs,
    checks: {
      index: { path: indexPath, present: repo.current.has(indexPath), required: requireIndex },
      dateSuffix: { newFiles: newDateDocs, passed: newDateDocs.length === 0 },
      artifacts: { newFiles: newArtifacts, passed: newArtifacts.length === 0 },
      activeSize: { bytes: activeBytes, files: active.length, oversized, passed: activeBytes <= maxActiveBytes && oversized.length === 0 },
    },
    errors,
  };
}

function parseArgs(argv) {
  const options = { cwd: process.cwd() };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === '--root') options.cwd = path.resolve(argv[++index]);
    else if (argument === '--max-active-bytes') options.maxActiveBytes = Number(argv[++index]);
    else if (argument === '--max-file-bytes') options.maxFileBytes = Number(argv[++index]);
    else if (argument === '--diff-base') options.baselineRef = argv[++index];
    else if (argument === '--no-index') options.requireIndex = false;
    else throw new Error(`unknown argument: ${argument}`);
  }
  for (const key of ['maxActiveBytes', 'maxFileBytes']) {
    if (options[key] !== undefined && (!Number.isSafeInteger(options[key]) || options[key] < 0)) throw new Error(`${key} must be a non-negative integer`);
  }
  return options;
}

async function main() {
  try {
    const report = await checkDocs(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
    if (!report.ok) process.exitCode = 1;
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 2;
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
