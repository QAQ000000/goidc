#!/usr/bin/env node
// Opt-in kernel-boundary probe. Connects the declared test-cluster Unix socket
// and immediately closes it; sends no PostgreSQL protocol message or SQL.
import { chmod, chown, copyFile, mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { execFileSync, spawnSync } from 'node:child_process';
import path from 'node:path';
import { namespaceArgs } from './accept-release-runtime.mjs';
import { assertSocket, requireThat } from './release-runtime-acceptance-lib.mjs';
const socket = process.argv[2];
requireThat(process.argv.length === 4 && process.argv[3] === '--execute' && process.getuid?.() === 0, 'Usage: root node scripts/probe-release-runtime-isolation.mjs ABSOLUTE_TEST_PG_SOCKET --execute');
const port = Number(path.basename(socket).replace('.s.PGSQL.', '')); await assertSocket(socket, port);
const account = execFileSync('/usr/bin/getent', ['passwd', 'www'], { encoding: 'utf8' }).trim().split(':');
const uid = Number(account[2]), gid = Number(account[3]); requireThat(uid > 0 && gid > 0, 'Ordinary www required');
const root = await mkdtemp('/dev/shm/zjmfgo-runtime-isolation-probe-');
try {
  await chmod(root, 0o755);
  for (const name of ['release/artifact', 'release/runtime', 'release/web/.next/cache', 'control', 'fixture', 'mutable/cache', 'socket']) await mkdir(path.join(root, name), { recursive: true, mode: 0o755 });
  await chown(path.join(root, 'mutable'), uid, gid); await chown(path.join(root, 'mutable/cache'), uid, gid);
  await copyFile(process.execPath, path.join(root, 'release/runtime/node'));
  const staged = path.join(root, 'socket', path.basename(socket)); await writeFile(staged, '', { mode: 0o644 });
  const args = namespaceArgs({ release: path.join(root, 'release'), control: path.join(root, 'control'), fixture: path.join(root, 'fixture'), mutable: path.join(root, 'mutable'), pgSocket: staged, uid, gid, appRoot: 'web' });
  const code = `const fs=require('fs'),net=require('net'),cp=require('child_process');const report={uid:process.getuid(),gid:process.getgid(),uidMap:fs.readFileSync('/proc/self/uid_map','utf8').trim(),sourceHidden:!fs.existsSync('/www/wwwroot/zjmfgo'),toolchainHidden:!fs.existsSync('/usr/bin/npm')&&!fs.existsSync('/usr/local/btgo/bin/go'),interfaces:fs.readFileSync('/proc/net/dev','utf8').split('\\n').filter(x=>x.includes(':')).map(x=>x.split(':')[0].trim())};try{fs.writeFileSync('/release/escape','x');throw Error('release writable')}catch(e){if(e.code!=='EROFS')throw e;report.readonlyRelease=true}const trace=cp.spawnSync('/usr/bin/strace',['-o','/mutable/probe.trace','-e','trace=%file,%network','/release/runtime/node','-e','require("fs").writeFileSync("/mutable/small","ok")'],{encoding:'utf8'});if(trace.status!==0)throw Error('strace failed');report.strace=true;const server=net.createServer(client=>{const up=net.createConnection(${JSON.stringify(`/pg/${path.basename(socket)}`)});up.once('error',()=>process.exit(3));up.once('connect',()=>{client.destroy();up.destroy();server.close(()=>{report.loopbackBridge=true;report.postgresSocketConnected=true;fs.writeFileSync('/mutable/report.json',JSON.stringify(report))})})});server.listen(56543,'127.0.0.1',()=>net.createConnection(56543,'127.0.0.1').on('error',()=>{}));setTimeout(()=>process.exit(2),5000).unref();`;
  const worker = `const cp=require('child_process');cp.execFileSync('/usr/bin/mount',['--bind',process.argv[1],process.argv[2]]);try{const r=cp.spawnSync('/usr/bin/setpriv',['--reuid',process.argv[3],'--regid',process.argv[4],'--clear-groups','--','/usr/bin/bwrap',...JSON.parse(process.argv[5])],{stdio:'ignore',timeout:10000});if(r.status!==0)process.exitCode=1}finally{cp.execFileSync('/usr/bin/umount',[process.argv[2]])}`;
  const result = spawnSync('/usr/bin/unshare', ['--mount', '--propagation', 'private', process.execPath, '-e', worker, socket, staged, String(uid), String(gid), JSON.stringify([...args, '/release/runtime/node', '-e', code])], { stdio: 'ignore', timeout: 15000 });
  requireThat(result.status === 0, 'Kernel isolation/socket/strace probe failed');
  const report = JSON.parse(await readFile(path.join(root, 'mutable/report.json'), 'utf8'));
  requireThat(report.uid === uid && report.gid === gid && report.sourceHidden && report.toolchainHidden && report.strace && report.readonlyRelease && report.postgresSocketConnected && JSON.stringify(report.interfaces) === '["lo"]', 'Probe boundaries not proven');
  console.log(JSON.stringify({ status: 'PASS', ...report, scope: 'small kernel probe only; no SQL, app candidate, service, or deployment acceptance' }));
} finally { await rm(root, { recursive: true, force: true }); }
