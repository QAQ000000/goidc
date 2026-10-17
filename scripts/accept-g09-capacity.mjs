#!/usr/bin/env node
// G09 capacity entry gate. It validates a sealed candidate and fixed profile;
// the execution controller is intentionally not present in this increment.
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { preflightG09Capacity } from './g09-capacity-contract.mjs';

const hash = /^[a-f0-9]{64}$/;
const required = new Set(['--profile', '--candidate', '--manifest-sha256', '--source', '--budget', '--budget-sha256', '--run-root']);

export function parseG09CapacityOptions(args) {
  const values = {};
  for (let i = 0; i < args.length; i++) {
    const key = args[i];
    if (key === '--execute') { if (values.execute) throw new Error('g09_capacity_cli_invalid'); values.execute = true; continue; }
    const name = key.slice(2).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
    if (!required.has(key) || values[name] !== undefined || i + 1 >= args.length || args[i + 1].startsWith('--')) throw new Error('g09_capacity_cli_invalid');
    values[name] = args[++i];
  }
  for (const key of required) {
    const name = key.slice(2).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
    if (values[name] === undefined) throw new Error('g09_capacity_cli_missing');
  }
  if (!path.isAbsolute(values.profile) || !path.isAbsolute(values.candidate) || !path.isAbsolute(values.source) || !path.isAbsolute(values.budget) || !path.isAbsolute(values.runRoot) || !hash.test(values.manifestSha256) || !hash.test(values.budgetSha256)) throw new Error('g09_capacity_cli_invalid');
  return Object.freeze(values);
}

export async function acceptG09Capacity(options) {
  const result = await preflightG09Capacity(options);
  if (options.execute) throw new Error('g09_capacity_controller_not_implemented');
  return result;
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    const options = parseG09CapacityOptions(process.argv.slice(2));
    const result = await acceptG09Capacity(options);
    process.stdout.write(`${JSON.stringify(result)}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}
