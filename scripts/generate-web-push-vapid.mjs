#!/usr/bin/env node
import { generateKeyPairSync } from 'node:crypto';
import { writeFileSync } from 'node:fs';
import path from 'node:path';

// Offline only. Secrets go to a new owner-readable file, never stdout.
if (process.argv.length !== 4 || process.argv[2] !== '--out') {
  console.error('Usage: node scripts/generate-web-push-vapid.mjs --out <private-json-file>');
  process.exit(1);
}
const { privateKey } = generateKeyPairSync('ec', { namedCurve: 'prime256v1' });
const key = privateKey.export({ format: 'jwk' });
const publicKey = Buffer.concat([Buffer.from([4]), Buffer.from(key.x, 'base64url'), Buffer.from(key.y, 'base64url')]).toString('base64url');
const destination = path.resolve(process.argv[3]);
try {
  writeFileSync(destination, JSON.stringify({ public_key: publicKey, private_key: key.d }, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
  console.log(`VAPID keys saved to ${destination}`);
} catch {
  console.error('Cannot create key file; choose a new file in an existing private directory.');
  process.exit(1);
}
