import { createHash } from 'node:crypto';
import { mkdir, readFile, stat, writeFile } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const DEFAULT_MANIFEST = 'audit/contract-gates.json';
const DEFAULT_OUTPUT = 'output/gates/contract-report.json';
const DATE_SUFFIX = /-\d{8}\.md$/i;

const exists = async (cwd, relative) => {
  try { await stat(path.join(cwd, relative)); return true; }
  catch { return false; }
};

const validRelative = value => typeof value === 'string'
  && value.length > 0
  && !path.isAbsolute(value)
  && !value.split('/').includes('..')
  && !value.includes('\\');

const gitCommit = cwd => {
  const result = spawnSync('git', ['-C', cwd, 'rev-parse', 'HEAD'], { encoding: 'utf8' });
  return result.status === 0 ? result.stdout.trim() : null;
};

export async function checkContractGates({ cwd = process.cwd(), manifestPath = DEFAULT_MANIFEST } = {}) {
  const errors = [];
  const addError = (pathName, message) => errors.push({ path: pathName, message });
  let raw;
  try { raw = await readFile(path.join(cwd, manifestPath), 'utf8'); }
  catch (error) {
    addError(manifestPath, `cannot read manifest: ${error.message}`);
    return { ok: false, manifestPath, commit: gitCommit(cwd), errors };
  }

  let manifest;
  try { manifest = JSON.parse(raw); }
  catch (error) {
    addError(manifestPath, `invalid JSON: ${error.message}`);
    return { ok: false, manifestPath, commit: gitCommit(cwd), errors };
  }

  if (manifest?.schemaVersion !== 1) addError(manifestPath, 'schemaVersion must be 1');
  if (!Array.isArray(manifest?.contracts) || manifest.contracts.length === 0) {
    addError(manifestPath, 'contracts must be a non-empty array');
  }

  const ids = new Set();
  for (const [index, entry] of (manifest.contracts ?? []).entries()) {
    const prefix = `${manifestPath}:contracts[${index}]`;
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
      addError(prefix, 'contract entry must be an object');
      continue;
    }
    if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(entry.id ?? '') || ids.has(entry.id)) addError(prefix, 'id must be unique kebab-case');
    ids.add(entry.id);
    if (!validRelative(entry.contract) || !entry.contract.startsWith('docs/') || !entry.contract.endsWith('.md') || DATE_SUFFIX.test(entry.contract)) {
      addError(`${prefix}.contract`, 'must be a stable relative Markdown path under docs/');
    } else if (!(await exists(cwd, entry.contract))) addError(`${prefix}.contract`, 'path does not exist');

    if (!Array.isArray(entry.implementation) || entry.implementation.length === 0) addError(`${prefix}.implementation`, 'must list at least one path');
    for (const [pathIndex, implementation] of (entry.implementation ?? []).entries()) {
      if (!validRelative(implementation)) addError(`${prefix}.implementation[${pathIndex}]`, 'must be a safe relative path');
      else if (!(await exists(cwd, implementation))) addError(`${prefix}.implementation[${pathIndex}]`, 'path does not exist');
    }

    if (!Array.isArray(entry.tests) || entry.tests.length === 0 || entry.tests.some(test => typeof test !== 'string' || test.trim() === '')) {
      addError(`${prefix}.tests`, 'must contain at least one non-empty command');
    }
    if (!Array.isArray(entry.evaluation) || entry.evaluation.some(item => typeof item !== 'string' || item.trim() === '')) addError(`${prefix}.evaluation`, 'must be an array of non-empty descriptions');
    if (entry.review?.required !== true || !Array.isArray(entry.review?.owners) || entry.review.owners.length === 0) addError(`${prefix}.review`, 'must require review and list owners');
    if (entry.releaseEvidence?.required !== true || typeof entry.releaseEvidence.kind !== 'string' || entry.releaseEvidence.kind.length === 0) addError(`${prefix}.releaseEvidence`, 'must require a named evidence kind');
  }

  const digest = createHash('sha256').update(raw).digest('hex');
  return {
    ok: errors.length === 0,
    manifestPath,
    commit: gitCommit(cwd),
    manifestSha256: digest,
    contractCount: manifest.contracts?.length ?? 0,
    errors,
  };
}

function parseArgs(argv) {
  const options = { cwd: process.cwd(), manifestPath: DEFAULT_MANIFEST, outputPath: DEFAULT_OUTPUT };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === '--root') options.cwd = path.resolve(argv[++index]);
    else if (argument === '--manifest') options.manifestPath = argv[++index];
    else if (argument === '--output') options.outputPath = argv[++index];
    else throw new Error(`unknown argument: ${argument}`);
  }
  return options;
}

async function main() {
  try {
    const options = parseArgs(process.argv.slice(2));
    const report = await checkContractGates(options);
    const output = path.join(options.cwd, options.outputPath);
    await mkdir(path.dirname(output), { recursive: true });
    await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
    process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
    if (!report.ok) process.exitCode = 1;
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 2;
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
