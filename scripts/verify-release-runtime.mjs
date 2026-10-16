#!/usr/bin/env node
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { verifyRuntimeCandidate } from './release-runtime-lib.mjs';

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    const args = process.argv.slice(2);
    if (args.length !== 3) throw new Error('Usage: node scripts/verify-release-runtime.mjs SEALED_ROOT TRUSTED_MANIFEST_SHA256 TRUSTED_CLEAN_SOURCE_REPO');
    const manifest = await verifyRuntimeCandidate(path.resolve(args[0]), args[1], path.resolve(args[2]));
    console.log(JSON.stringify({ verified: true, format: manifest.format, commit: manifest.source.commit, buildID: manifest.web.buildID, files: manifest.artifacts.length, scope: 'sealed runtime package only; no startup, service health or deployment verification' }));
  } catch (error) { console.error(`[release-runtime] ${error.message}`); process.exitCode = 1; }
}
