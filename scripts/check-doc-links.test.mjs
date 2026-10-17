import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, rm } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const script = fileURLToPath(new URL('./check-doc-links.mjs', import.meta.url));

test('reports broken relative links and accepts anchors and external URLs', async () => {
  const cwd = await mkdtemp(path.join(os.tmpdir(), 'doc-links-'));
  try {
    await mkdir(path.join(cwd, 'docs'), { recursive: true });
    await writeFile(path.join(cwd, 'README.md'), '[ok](docs/INDEX.md) [external](https://example.com)\n');
    await writeFile(path.join(cwd, 'docs/INDEX.md'), '# index\n[anchor](#index)\n[missing](missing.md)\n');
    const result = spawnSync(process.execPath, [script, cwd], { encoding: 'utf8' });
    assert.equal(result.status, 1);
    const report = JSON.parse(result.stdout);
    assert.equal(report.files, 2);
    assert.deepEqual(report.missing, [{ file: 'docs/INDEX.md', target: 'missing.md' }]);
  } finally {
    await rm(cwd, { recursive: true, force: true });
  }
});
