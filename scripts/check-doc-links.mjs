#!/usr/bin/env node
import { readdir, readFile, stat } from 'node:fs/promises';
import path from 'node:path';

const root = path.resolve(process.argv[2] ?? process.cwd());

async function markdownFiles(directory) {
  const result = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const file = path.join(directory, entry.name);
    if (entry.isDirectory()) result.push(...await markdownFiles(file));
    else if (entry.isFile() && file.endsWith('.md')) result.push(file);
  }
  return result;
}

async function exists(file) {
  try {
    const info = await stat(file);
    return info.isFile() || info.isDirectory();
  } catch {
    return false;
  }
}

function localTarget(value) {
  const target = value.trim();
  if (!target || /^(?:https?:|mailto:|#|\/)/i.test(target)) return null;
  return target.split('#')[0].replace(/:\d+$/, '');
}

const files = [path.join(root, 'README.md'), ...(await markdownFiles(path.join(root, 'docs')))];
const missing = [];
for (const file of files) {
  const source = await readFile(file, 'utf8');
  for (const match of source.matchAll(/\[[^\]]*\]\(([^)]+)\)/g)) {
    const target = localTarget(match[1]);
    if (!target || await exists(path.resolve(path.dirname(file), target))) continue;
    missing.push({ file: path.relative(root, file), target });
  }
}

const report = { root, files: files.length, missing, ok: missing.length === 0 };
process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
if (!report.ok) process.exitCode = 1;
