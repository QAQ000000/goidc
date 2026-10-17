// Read-only recovery after the namespace has stopped. Never infer an ack from
// a filename, advance an unknown command, or send a command from this helper.
import { lstat, readdir } from 'node:fs/promises';
import { noLinks } from './release-candidate-lib.mjs';
import { readG09ControlJSON, validateG09Command } from './g09-runtime-contract.mjs';

const unknown = () => Object.freeze({ status: 'unknown', frozen: true, reason: 'g09_control_history_unknown' });
const terminal = action => action === 'finish' || action === 'abort';
const historyNames = names => names.filter(name => /^(?:command|result)-/.test(name)).sort();

// Only status=ready exposes a state that the outer controller may use to send
// abort. failed/terminal/unknown must never be used to generate another command.
// Caller must first stop the namespace and prohibit all further command writers.
export async function recoverG09ControlHistory(directory, runId, { uid = process.getuid() } = {}) {
  try {
    validateG09Command({ runId, sequence: 1, action: 'abort' }, runId, 1);
    await noLinks(directory);
    const info = await lstat(directory);
    if (!info.isDirectory() || info.uid !== uid || (info.mode & 0o777) !== 0o700) return unknown();
    const names = historyNames(await readdir(directory));
    const commands = new Map(), results = new Map();
    for (const name of names) {
      const match = name.match(/^(command|result)-([1-9][0-9]*)\.json$/);
      if (!match) return unknown();
      const sequence = Number(match[2]);
      if (!Number.isSafeInteger(sequence) || sequence > 32) return unknown();
      (match[1] === 'command' ? commands : results).set(sequence, name);
    }
    if (commands.size !== results.size) return unknown();
    let last = null;
    for (let sequence = 1; sequence <= commands.size; sequence++) {
      if (!commands.has(sequence) || !results.has(sequence) || last?.ok === false || terminal(last?.action)) return unknown();
      const request = validateG09Command(await readG09ControlJSON(`${directory}/${commands.get(sequence)}`, { uid }), runId, sequence);
      const result = await readG09ControlJSON(`${directory}/${results.get(sequence)}`, { uid });
      if (!result || Array.isArray(result) || result.runId !== runId || result.sequence !== sequence || result.action !== request.action || typeof result.ok !== 'boolean') return unknown();
      last = result;
    }
    // A fixture result may become visible while scanning. Such a changing
    // inventory is unknown; the caller can retain the failure for evidence.
    if (JSON.stringify(names) !== JSON.stringify(historyNames(await readdir(directory)))) return unknown();
    if (last?.ok === false) return Object.freeze({ status: 'failed', frozen: true, sequence: commands.size, action: last.action });
    if (terminal(last?.action)) return Object.freeze({ status: 'terminal', frozen: true, sequence: commands.size, action: last.action });
    if (commands.size >= 32) return unknown();
    return Object.freeze({ status: 'ready', frozen: false, state: Object.freeze({ runId, sequence: commands.size }) });
  } catch { return unknown(); }
}
