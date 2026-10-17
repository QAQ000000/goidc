import { readFile, readdir, writeFile, mkdir } from 'node:fs/promises';
import path from 'node:path';
import { createHash } from 'node:crypto';

// Deterministic static inventory. No execution of application code or environment files.
const ledgerPath = process.argv[2] ?? 'docs/admin-full-ui-restore-ledger.md';
const specPath = process.argv[3] ?? '/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md';
const ledger = await readFile(ledgerPath, 'utf8');
const spec = await readFile(specPath, 'utf8');
const browserEvidencePath = process.argv[4] ?? 'docs/admin-restore-browser-evidence.json';
let browserEvidence = {pages:[]};
try { browserEvidence = JSON.parse(await readFile(browserEvidencePath,'utf8')); }
catch(error) { if(error.code !== 'ENOENT') throw error; }
if(!Array.isArray(browserEvidence.pages)) throw Error('browser evidence pages must be an array');
const server = await readFile('internal/httpapi/server.go', 'utf8');
const rbac = await readFile('internal/store/admin_rbac.go', 'utf8');
async function files(dir) {
  const result = [];
  for (const entry of (await readdir(dir, {withFileTypes:true})).sort((a,b)=>a.name.localeCompare(b.name, 'en'))) {
    const p = path.join(dir, entry.name);
    if (entry.isDirectory()) result.push(...await files(p)); else result.push(p);
  }
  return result;
}
const pageFiles = (await files('web/src/app/admin')).filter(p=>p.endsWith('/page.tsx'));
const storeFiles = (await files('internal/store')).filter(p=>p.endsWith('.go')&&!p.endsWith('_test.go'));
const stores = await Promise.all(storeFiles.map(async file=>({file,text:await readFile(file,'utf8')})));
const migrationFiles = (await files('db/migrations')).filter(p=>p.endsWith('.sql'));
const migrations = await Promise.all(migrationFiles.map(async file=>({file,text:await readFile(file,'utf8')})));
const registered = [...server.matchAll(/mux\.HandleFunc\("(GET|POST|PUT|PATCH|DELETE) \/api\/v1(\/admin[^"\s]*)",\s*s\.(?:auth|sessionAuth)\(s\.(\w+)/g)].map(m=>({method:m[1],path:m[2],handler:m[3],evidence:`internal/httpapi/server.go:${server.slice(0,m.index).split('\n').length}`}));
const headings = [...spec.matchAll(/^####\s+(\d+)\.\s+(.*?)\s+\(`(#[^`]+)`\)/gm)];
const contracts = [];
for (const [i,line] of ledger.split(/\r?\n/).entries()) {
  const cells = line.split('|').map(s=>s.trim());
  if (!/^\d+$/.test(cells[1]??'') || !cells[2]?.includes('#/')) continue;
  const index = Number(cells[1]);
  const oldHash = cells[2].match(/`(#[^`]+)`/)?.[1];
  const title = cells[2].replace(/\s*`#[^`]+`/, '').trim();
  const heading = headings.find(h=>h[3]===oldHash);
  if (!heading) throw Error(`missing specification for ${oldHash}`);
  const pos = headings.indexOf(heading);
  const block = spec.slice(heading.index,headings[pos+1]?.index??spec.length);
  const specLine = spec.slice(0,heading.index).split(/\r?\n/).length;
  const module = spec.slice(0,heading.index).match(/### 【模块】([^（\n]+)/g)?.at(-1)?.replace('### 【模块】','').trim();
  const newRoute = cells[3].replaceAll('`','');
  const routePath = newRoute.split('?')[0].replace(/\{([^}]+)\}/g,'[$1]');
  const pageFile = pageFiles.find(p=>p===`web/src/app${routePath}/page.tsx`)??null;
  const pageText = pageFile ? await readFile(pageFile,'utf8') : '';
  const imports = [...pageText.matchAll(/import\s+(?:\{([^}]+)\}|([A-Za-z_$][\w$]*))\s+from\s+["']([^"']+)["']/g)].flatMap(m=>(m[1]?m[1].split(',').map(s=>s.trim().split(/\s+as\s+/).at(-1)):[m[2]]).map(name=>({name,source:m[3]})));
  const jsxNames = [...new Set([...pageText.matchAll(/<([A-Z][\w$]*)\b/g)].map(m=>m[1]))];
  const component = jsxNames.map(name=>({name,source:imports.find(i=>i.name===name)?.source??'页面内定义或未验证',evidence:pageFile?`${pageFile}:${pageText.slice(0,pageText.indexOf('<'+name)).split('\n').length}`:null}));
  const pageBrowserEvidence = browserEvidence.pages.filter(e=>e.newRoute===newRoute);
  const acceptedWidths = new Set(pageBrowserEvidence.filter(e=>e.result==='PASS').map(e=>e.width));
  const integrityStatuses = [...new Set(pageBrowserEvidence.map(e=>e.screenshotIntegrity?.status).filter(Boolean))];
  const integrityNotice = [integrityStatuses.includes('mismatch')?'截图失配需重验':null,integrityStatuses.includes('missing_hash')?'截图摘要未建立':null].filter(Boolean).join('；');
  const browserAcceptance = (acceptedWidths.size === 4 ? '历史PASS（四尺寸）' : acceptedWidths.size ? `历史部分（${acceptedWidths.size}/4）` : '未验证') + (integrityNotice?`；${integrityNotice}`:'');
  const sourceLines = pattern => block.split('\n').filter(l=>pattern.test(l)).map(l=>l.trim());
  const backtick = /`([^`]+)`/g;
  const extract = (pattern) => [...block.split('\n').filter(l=>pattern.test(l)).flatMap(l=>[...l.matchAll(backtick)].map(m=>m[1]))];
  const claimedApi = cells[5].replaceAll('`','');
  const apiClaims = [...claimedApi.matchAll(/(GET|POST|PUT|PATCH|DELETE)(?:\/(?:GET|POST|PUT|PATCH|DELETE))*\s+(\/admin[^\s+`，]+)/g)].map(m=>({methods:m[0].split(/\s+/)[0].split('/'),path:m[2].split('?')[0]}));
  const pathPattern = value => value.split(/(\*|\{[^}]+\})/).map(piece=>piece==='*'?'.*':piece.startsWith('{')?'[^/]+':piece.replace(/[.*+?^${}()|[\]\\]/g,'\\$&')).join('');
  const api = registered.filter(r=>apiClaims.some(c=>c.methods.includes(r.method)&&new RegExp('^'+pathPattern(c.path)+'$').test(r.path.replace(/\{[^}]+\}/g,'parameter'))));
  const permissionClaim = cells[6].split(' / ')[0].replaceAll('`','');
  const tokens = permissionClaim==='@super'?['@super']:permissionClaim.split(/[\s,]+/).flatMap(t=>{ const [base,...suffixes]=t.split('/'); return [base,...suffixes.map(s=>base.replace(/\.[^.]+$/,'.'+s))];});
  const permissionEvidence = tokens.map(token=>({token,status:token==='@super'?'未验证':rbac.includes(`"${token}"`)?'已登记':'未验证',evidence:token==='@super'?null:rbac.includes(`"${token}"`)?`internal/store/admin_rbac.go:${rbac.slice(0,rbac.indexOf(`"${token}"`)).split('\n').length}`:null}));
  const storageEvidence = stores.filter(s=>api.some(a=>new RegExp(`func \\(s \\*Store\\) ${a.handler[0].toUpperCase()+a.handler.slice(1)}\\b`).test(s.text))).map(s=>s.file);
  const tableNames = [...new Set(stores.filter(s=>storageEvidence.includes(s.file)).flatMap(s=>[...s.text.matchAll(/\b(?:FROM|JOIN|INTO|UPDATE)\s+([a-z_][a-z_0-9]*)/g)].map(m=>m[1])))].sort();
  const migrationEvidence = migrations.filter(m=>tableNames.some(t=>new RegExp(`\\b${t}\\b`).test(m.text))).map(m=>m.file);
  contracts.push({index,legacyNumber:Number(heading[1]),oldHash,title,module,newRoute,pageFile,component:component.length?component:'未验证',
    pageStatus:{ledger:cells[4],code:pageFile?'有对应路由源码；实现完整性未验证':'未找到对应独立路由文件；实现状态未验证',browserAcceptance},
    evidence:{specification:`${specPath}:${specLine}`,ledger:`${ledgerPath}:${i+1}`,component:pageFile?`${pageFile}:1`:null},
    realApi:{ledgerClaim:claimedApi,registered:api,status:api.length?'已找到静态路由；逐动作与权限链未验证':'未验证'},
    permission:{ledgerClaim:permissionClaim,registry:permissionEvidence,enforcement:'未验证'},
    storage:{tables:tableNames,evidence:storageEvidence,status:storageEvidence.length?'静态候选，调用链未验证':'未验证'},
    migrations:{evidence:migrationEvidence,status:migrationEvidence.length?'表名关联候选，逐页适用性未验证':'未验证'},
    fields:{legacy:extract(/列定义|表单|字段.*控件/),sourceLines:sourceLines(/列定义|字段|表单|控件类型|输入控件/),current:'未验证'},
    filters:{legacy:extract(/筛选项/),current:'未验证'},
    bulkActions:{legacy:extract(/批量/),current:'未验证'},
    dialogs:{legacy:block.split('\n').filter(l=>/弹窗：|抽屉：/.test(l)).map(l=>l.trim()),current:'未验证'},
    validations:{legacy:block.split('\n').filter(l=>/校验|必填|验证规则/.test(l)).map(l=>l.trim()),current:'未验证'},
    stateCoverage:{loading:'未验证',empty:'未验证',error:'未验证',permissionDenied:'未验证',conflict:'未验证',success:'未验证'},
    stateEvidence:pageBrowserEvidence.map(e=>({width:e.width,height:e.height,checks:e.checks??{},result:e.result??'未验证',resultScope:e.scope??null,resultInterpretation:'历史记录；本生成器未运行浏览器或校验截图。',screenshot:e.screenshot??null,screenshotSha256:e.screenshotSha256??null,screenshotIntegrity:e.screenshotIntegrity??null,source:browserEvidencePath})),
    viewportResults:[{width:1440,height:900},{width:1024,height:768},{width:390,height:844},{width:320,height:800}].map(size=>{
      const matches=pageBrowserEvidence.filter(e=>e.width===size.width);
      return matches.length?{...size,height:matches.at(-1).height??size.height,result:matches.at(-1).result??'未验证',resultInterpretation:'历史记录；截图声明失配或未登记摘要时，不可用于本次完整性证明。',screenshots:matches.map(e=>e.screenshot).filter(Boolean),evidence:matches.map(e=>({source:browserEvidencePath,checks:e.checks??{},scope:e.scope??null,screenshotSha256:e.screenshotSha256??null,screenshotIntegrity:e.screenshotIntegrity??null}))}:{...size,result:'未验证',screenshots:[]};
    }),
    note:oldHash==='#/login'?'特殊记录：原规范名称为控制台概览但内容为登录；新台账 /admin 不是原登录页验收证明。':newRoute.includes('?')?'共享路由的 query 子视图需单独验收；文件存在不证明筛选/页签已实现。':'未运行浏览器；未验证项不得解释为功能不存在或验收通过。'
  });
}
if(contracts.length!==105||new Set(contracts.map(p=>p.oldHash)).size!==105)throw Error('expected exactly 105 distinct legacy hashes');
const output={schemaVersion:1,source:{ledger:ledgerPath,ledgerSha256:createHash('sha256').update(ledger).digest('hex'),specification:specPath,specificationSha256:createHash('sha256').update(spec).digest('hex'),browserEvidence:browserEvidencePath,browserEvidenceIntegrityReview:browserEvidence.integrityReview??null},notes:['无时间戳；同一源码和输入可复现。','台账声称的 API 与源码注册 API 分开；权限登记不等于强制执行。','源码存在仅作为静态路由证据，编译和实现完整性未验证；验收宽度为1440/1024/390/320。','storage/migrations 为保守静态候选，缺少确切调用链时未验证。','result/checks/PASS仅为历史检查记录；screenshotSha256保留原值，screenshotIntegrity保留失配或摘要未建立声明。生成器不读取截图，当前完整性须另运行只读校验器。'],count:contracts.length,pages:contracts};
await mkdir('docs/generated',{recursive:true});
await writeFile('docs/generated/admin-page-contracts.json',JSON.stringify(output,null,2)+'\n');
const lines=['# 后台 105 页页面契约索引','','运行 `node scripts/admin-page-contracts.mjs [台账路径] [旧版规范路径] [浏览器证据JSON路径]` 可复现生成 JSON 和本索引。旧规范默认使用现有本地文档；不读取环境配置或部署密钥。','','[完整机器可读契约](./generated/admin-page-contracts.json) 包含旧版字段、筛选、批量动作、弹窗、校验、真实注册 API 与证据、权限登记、存储及迁移候选、状态和四尺寸验收。源码存在不等于完成；本生成器没有执行编译或浏览器，1440/1024/390/320 四尺寸及无浏览器证据的结果为未验证。可选 `docs/admin-restore-browser-evidence.json` 按精确 newRoute 和宽度绑定验收与截图，仍不自动改变整体页面完成状态。旧规范绝对路径仅用于来源引用，并非部署配置。','','| # | 模块 / 页面 | 旧 hash | 新路由 | 代码状态 | 浏览器 |','|---:|---|---|---|---|---|'];
lines.splice(6,0,'浏览器列和 JSON 的 result/checks 保留历史验收范围；截图失配或摘要未建立单独标注，不能据此证明本次完整性。生成器不读取截图，运行 `node scripts/check-admin-browser-evidence.mjs --root 截图所在项目根目录 --strict` 只读复核当前文件和摘要。','');
for(const p of contracts)lines.push(`| ${p.index} | [${p.module} / ${p.title}](#page-${p.index}) | ${p.oldHash} | ${p.newRoute} | ${p.pageStatus.code} | ${p.pageStatus.browserAcceptance} |`);
for(const p of contracts)lines.push('',`<a id="page-${p.index}"></a>`,`## ${p.index}. ${p.title}`,'',`旧规范：[源文档](${p.evidence.specification})；台账：[记录](../${p.evidence.ledger})${p.evidence.component?`；页面：[源码](../${p.evidence.component})`:''}。JSON 中按唯一旧 hash ${p.oldHash} 检索。`);
await writeFile('docs/admin-page-contracts.md',lines.join('\n')+'\n');
console.log(JSON.stringify({count:contracts.length,uniqueHashes:new Set(contracts.map(p=>p.oldHash)).size,registeredApiPages:contracts.filter(p=>p.realApi.registered.length).length,routeSourcePages:contracts.filter(p=>p.evidence.component).length,historicalFourViewportPassPages:contracts.filter(p=>p.pageStatus.browserAcceptance.startsWith('历史PASS（四尺寸）')).length}));
