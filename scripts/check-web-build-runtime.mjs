#!/usr/bin/env node
import { readdir, readFile, realpath } from 'node:fs/promises';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

function isNextRuntime(args) {
  if (/^next-server(?:\s|$)/.test(args[0] ?? '')) return true;
  // Inspect the executable / Node script position, not arbitrary test arguments
  // or shell source text that happens to mention "next start".
  return [0, 1].some(index => path.basename(args[index] ?? '') === 'next' && ['start', 'dev'].includes(args[index + 1]));
}

export async function checkWebBuildRuntime(directory = process.cwd()) {
  const web = await realpath(directory);
  if (process.platform !== 'linux') {
    return { unsupported: true, reason: '当前平台没有 Linux /proc 运行目录检查；请确认此独立目录没有运行中的 Next 服务。', pids: [] };
  }
  let entries;
  try { entries = await readdir('/proc'); }
  catch (error) {
    if (error.code === 'ENOENT' || error.code === 'ENOTDIR') {
      return { unsupported: true, reason: '未挂载 /proc，无法检查 Next 进程；请确认此独立目录没有运行中的 Next 服务。', pids: [] };
    }
    throw new Error('无法读取 /proc，已停止构建；请先核实运行目录。');
  }
  const pids = [];
  for (const pid of entries) {
    if (!/^\d+$/.test(pid) || Number(pid) === process.pid) continue;
    let cwd;
    try { cwd = await realpath(`/proc/${pid}/cwd`); }
    catch (error) {
      if (['ENOENT', 'ESRCH'].includes(error.code)) continue;
      // Hidden processes may include a runtime sharing this directory.
      throw new Error(`无法核对 PID ${pid} 的运行目录，已停止构建；请使用可检查进程的账户。`);
    }
    if (cwd !== web) continue;
    let args;
    try { args = (await readFile(`/proc/${pid}/cmdline`, 'utf8')).split('\0').filter(Boolean); }
    catch (error) {
      if (['ENOENT', 'ESRCH'].includes(error.code)) continue;
      throw new Error(`无法核对同目录 PID ${pid} 的进程类型，已停止构建。`);
    }
    if (isNextRuntime(args)) pids.push(Number(pid));
  }
  return { unsupported: false, pids };
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    const result = await checkWebBuildRuntime();
    if (result.unsupported) console.warn(`[web-build] ${result.reason}`);
    else if (result.pids.length) {
      console.error(`[web-build] 拒绝构建：此真实 Web 目录正在被 Next 服务使用（PID ${result.pids.join(', ')}）。next build 会覆盖运行中的 .next；请在独立 worktree / 发布目录构建，勿复用运行目录或其 .next。`);
      process.exitCode = 1;
    }
  } catch (error) {
    console.error(`[web-build] ${error.message}`);
    process.exitCode = 1;
  }
}
