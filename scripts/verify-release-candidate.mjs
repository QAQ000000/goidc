#!/usr/bin/env node
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { verifyCandidate } from './release-candidate-lib.mjs';

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    const args = process.argv.slice(2);
    if (![2, 3].includes(args.length)) throw new Error('Usage: node scripts/verify-release-candidate.mjs CANDIDATE TRUSTED_MANIFEST_SHA256 [TRUSTED_CLEAN_SOURCE_REPO]');
    const manifest = await verifyCandidate(path.resolve(args[0]), args[1], args[2]);
    console.log(JSON.stringify({ verified: true, commit: manifest.source.commit, buildID: manifest.web.buildID, files: manifest.artifacts.length, scope: 'sealed local candidate only; not runtime/deployment verification' }));
  } catch (error) { console.error(`[release-candidate] ${error.message}`); process.exitCode = 1; }
}
