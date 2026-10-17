#!/usr/bin/env node
import { parseArgs } from 'node:util';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { runG09Load, performG09Fetch, validateG09Profile } from './g09-load-lib.mjs';

// One executable read-load path. Business scenarios and worker recovery remain
// separate integration tests; this report makes no production capacity claim.
export async function runReadLoad({ origin, seconds = 30, rps = 10, concurrency = 20, signal } = {}) {
  const profile = {
    origin, maxConcurrency: Number(concurrency), maxOperations: 100000,
    requestTimeoutMs: 5000, maxScheduleLagMs: 100, drainTimeoutMs: 6000,
    phases: [{ name: 'steady', durationMs: Number(seconds) * 1000,
      streams: [{ category: 'catalog', unit: 'http_request', ratePerSecond: Number(rps) }] }],
  };
  validateG09Profile(profile);
  const report = await runG09Load({ profile, signal,
    perform: request => performG09Fetch({ ...request, pathname: '/api/v1/catalog' }),
  });
  return { scope: 'catalog_http_read', capacityVerified: false, ...report };
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    const { values } = parseArgs({ options: {
      origin: { type: 'string' }, seconds: { type: 'string' },
      rps: { type: 'string' }, concurrency: { type: 'string' },
    } });
    const controller = new AbortController();
    const stop = () => controller.abort();
    process.once('SIGINT', stop);
    process.once('SIGTERM', stop);
    try {
      const report = await runReadLoad({ ...values, signal: controller.signal });
      process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
      if (!report.ok || !report.total.succeeded) process.exitCode = 1;
    } finally {
      process.removeListener('SIGINT', stop);
      process.removeListener('SIGTERM', stop);
    }
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}
