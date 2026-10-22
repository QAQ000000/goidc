import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { createRequire, Module } from 'node:module';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('../web/', import.meta.url)), require = createRequire(path.join(web, 'package.json'));
const ts = require('typescript'), React = require('react'), cache = new Map();
let current, actor, liveActor, management, calls, storage, respond, failStorage, refreshes;
const changed = (a, b) => !a || !b || a.length !== b.length || a.some((value, index) => value !== b[index]);
const hooks = {
  ...React,
  useState(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = typeof initial === 'function' ? initial() : initial; return [state.values[index], next => { state.values[index] = typeof next === 'function' ? next(state.values[index]) : next; }]; },
  useRef(initial) { const state = current, index = state.cursor++; if (!(index in state.values)) state.values[index] = { current: initial }; return state.values[index]; },
  useEffect(effect, deps) { const state = current, index = state.cursor++, previous = state.values[index]; if (changed(previous?.deps, deps)) state.effects.push(() => { previous?.cleanup?.(); state.values[index] = { deps, cleanup: effect() }; }); },
};
class ApiError extends Error { constructor(status, code) { super(code); this.status = status; this.code = code; } }
const state = () => ({ cursor: 0, values: [], effects: [] });
function within(state, action) { const before = current; current = state; state.cursor = 0; let result; try { result = action(); } finally { current = before; } state.effects.splice(0).forEach(effect => effect()); return result; }
const ui = new Proxy({}, { get: (_, name) => props => React.createElement(({ Button: 'button', Input: 'input', Textarea: 'textarea', Checkbox: 'checkbox' })[name] ?? String(name).toLowerCase(), props, props.children) });
function load(filename) {
  if (cache.has(filename)) return cache.get(filename).exports;
  const module = new Module(filename); cache.set(filename, module); const external = createRequire(filename);
  module.require = name => {
    if (name === 'react') return hooks;
    if (name === './workspace') return { useAdminIdentity: () => actor, useAdminPermission: permission => actor?.role === 'super_admin' || actor?.permissions.includes(permission) };
    if (name.includes('/ui/')) return ui;
    if (name === '@/lib/api') return { ApiError, date: value => value, api: async (url, options = {}) => { const call = { url, ...options }; calls.push(call); return respond(call); } };
    if (name.startsWith('@/') || name.startsWith('.')) {
      const base = name.startsWith('@/') ? path.join(web, 'src', name.slice(2)) : path.resolve(path.dirname(filename), name);
      const source = [base, `${base}.tsx`, `${base}.ts`].find(candidate => existsSync(candidate)); if (source && /\.tsx?$/.test(source)) return load(source);
    }
    return external(name);
  };
  const compiled = ts.transpileModule(readFileSync(filename, 'utf8'), { reportDiagnostics: true, compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } });
  assert.equal((compiled.diagnostics ?? []).filter(item => item.category === ts.DiagnosticCategory.Error).length, 0);
  module._compile(compiled.outputText, filename); return module.exports;
}
const { useServiceStatusCorrection, latestLifecycleJob } = load(path.join(web, 'src/components/admin-service-status-mutation.ts'));
const { AdminServiceStatusCorrection } = load(path.join(web, 'src/components/admin-service-status-correction.tsx'));
const service = { id:31, user_id:41, owner_generation:0, version:3, status:'active', provision_mode:'manual', billing_cycle:'monthly' };
const base = (overrides = {}) => ({ service_id:31, customer_id:41, service_version:3, owner_generation:0, status:'active', billing_cycle:'monthly', pricing_mode:'contract', fixed_amount_cents:null, contract_base_cents:1000, contract_discount_cents:200, contract_amount_cents:800, amount_cents:800, next_due_at:'2026-11-01T00:00:00Z', term_mode:'follow_supplier', supplier_next_due_at:null, supplier_observed_at:null, ...overrides });
const job = (overrides = {}) => ({ id:51, action:'suspend', status:'succeeded', completed_at:'2026-01-01T00:00:00.000123Z', ...overrides });
let mode, jobs, detailOverrides;
const detail = () => ({ id:31, user_id:41, owner_generation:0, version:management.service_version, status:management.status, provision_mode:mode, jobs, ...detailOverrides });
const quote = body => ({ service_id:31, customer_id:41, service_version:body.expected_version, current_status:management.status, target_status:body.target_status, source_job_id:body.source_job_id, source_action:body.target_status==='suspended'?'suspend':'resume', source_completed_at:job().completed_at, provision_mode:mode, expires_at:new Date(Date.now()+60000).toISOString(), quote_hash:'a'.repeat(64) });
const receipt = call => { const body=JSON.parse(call.body);return { service_id:31, customer_id:41, service_version:body.expected_version+1, before_status:body.target_status==='suspended'?'active':'suspended', status:body.target_status, source_job_id:body.source_job_id, source_action:body.target_status==='suspended'?'suspend':'resume', replayed:false }; };
function reset() {
 actor={id:7,role:'admin',permissions:['services.view','services.status.correct'],customer_scope:'all',customer_ownerless:true};liveActor=actor;management=base();mode='manual';jobs=[job(),job({id:52,action:'sync'})];detailOverrides={};calls=[];storage=new Map();failStorage=false;refreshes=0;
 respond=async call=>call.url==='/me'?liveActor:call.url.endsWith('/management')?management:call.url==='/admin/services/31'?detail():call.url.endsWith('/status-correction-quotes')?quote(JSON.parse(call.body)):receipt(call);
 globalThis.window={setTimeout:(fn,delay)=>{const t=setTimeout(fn,delay);t.unref?.();return t;},clearTimeout,addEventListener(){},removeEventListener(){}};
 globalThis.sessionStorage={getItem:key=>storage.get(key)??null,setItem:(key,value)=>{if(failStorage)throw new Error('storage unavailable');storage.set(key,value);},removeItem:key=>storage.delete(key)};
}
const settle=()=>new Promise(resolve=>setTimeout(resolve,10));
async function ready() { const instance=state(),view={render:()=>within(instance,()=>useServiceStatusCorrection(actor,service,0)),unmount:()=>instance.values.forEach(v=>v?.cleanup?.())};view.render();await settle();view.render();return view; }
const writes=()=>calls.filter(c=>c.headers?.['Idempotency-Key']);
const quotes=()=>calls.filter(c=>c.url.endsWith('/status-correction-quotes'));
async function prepare(view) {const facts=await view.render().read();assert.ok(facts);const input=await view.render().preview(facts,51,'verified projection error','original completion audit and customer ticket');assert.ok(input);return input;}

test('independent status permission can preview read-only and write a frozen exact body',async()=>{
 reset();const view=await ready(),input=await prepare(view);assert.equal(writes().length,0);assert.equal(input.quote.source_job_id,51);assert.equal(input.quote.source_action,'suspend');assert.ok(await view.render().run(input));assert.equal(writes().length,1);assert.deepEqual(JSON.parse(writes()[0].body),input.body);assert.equal(storage.size,0);assert.ok(calls.every(c=>c.portal==='admin'));view.unmount();
});
test('unknown write survives refresh, expired quote, changed status/mode/source and replays exact body/key',async()=>{
 reset();let view=await ready();const input=await prepare(view),standard=respond;respond=async call=>{if(call.headers)throw new TypeError('response lost');return standard(call);};await view.render().run(input);const first=writes()[0];view.unmount();
 const [key,raw]=[...storage][0],saved=JSON.parse(raw);saved.quote.expires_at='2026-01-01T00:00:00Z';storage.set(key,JSON.stringify(saved));management=base({status:'terminated',service_version:8});mode='supplier';jobs=[job({id:99,action:'terminate'})];view=await ready();respond=async call=>call.headers?{...receipt(call),replayed:true}:standard(call);const result=await view.render().run();assert.ok(result);assert.equal(result.receipt.replayed,true);assert.equal(result.facts.management.status,'terminated');assert.equal(writes()[1].body,first.body);assert.equal(writes()[1].headers['Idempotency-Key'],first.headers['Idempotency-Key']);assert.equal(storage.size,0);view.unmount();
});
test('latest lifecycle attempt includes failures/cancellations and ignores later sync; no old-success fallback',async()=>{
 assert.equal(latestLifecycleJob([job(),job({id:52,action:'sync'})]).id,51);
 for(const newer of [job({id:53,status:'failed'}),job({id:53,status:'cancelled'}),job({id:53,action:'terminate'}),job({id:53,action:'create'})]){reset();jobs.push(newer);const view=await ready(),facts=await view.render().read();assert.equal(await view.render().preview(facts,51,'reason','evidence'),null);assert.equal(quotes().length,0);view.unmount();}
});
test('unsupported mode/terminal state, source mismatch and incomplete completion cannot preview',async()=>{
 for(const change of [()=>{mode='supplier';},()=>{mode='nokvm';},()=>{management=base({status:'terminated'});},()=>{jobs=[job({action:'resume'})];},()=>{jobs=[job({completed_at:null})];}]){reset();change();const view=await ready(),facts=await view.render().read();assert.equal(await view.render().preview(facts,51,'reason','evidence'),null);assert.equal(quotes().length,0);view.unmount();}
});
test('bad input and substituted body/quote are rejected before write',async()=>{
 reset();const view=await ready(),facts=await view.render().read();for(const args of [[0,'reason','evidence'],[51,'','evidence'],[51,'r'.repeat(501),'evidence'],[51,'reason','e'.repeat(2001)],[51,'reason','bad\0']])assert.equal(await view.render().preview(facts,...args),null);assert.equal(quotes().length,0);const input=await prepare(view);for(const bad of [{...input,body:{...input.body,reason:'changed'}},{...input,body:{...input.body,evidence:'changed'}},{...input,body:{...input.body,target_status:'active'}},{...input,body:{...input.body,extra:true}},{...input,quote:{...input.quote,source_job_id:52}}])assert.equal(await view.render().run(bad),null);assert.equal(writes().length,0);view.unmount();
});
test('response identity, ownership, status and completion timestamp mismatches fail closed',async()=>{
 for(const override of [{user_id:42},{owner_generation:2},{version:99},{status:'suspended'},{jobs:[job(),job()]}]){reset();detailOverrides=override;const view=await ready();assert.equal(await view.render().read(),null);assert.equal(writes().length,0);view.unmount();}
 for(const override of [{customer_id:42},{service_id:32},{source_action:'resume'},{source_completed_at:'2026-01-01T00:00:00.000124Z'},{provision_mode:'simulation'},{current_status:'suspended'},{quote_hash:'bad'}]){reset();const view=await ready(),facts=await view.render().read(),standard=respond;respond=async call=>call.url.endsWith('/status-correction-quotes')?{...quote(JSON.parse(call.body)),...override}:standard(call);assert.equal(await view.render().preview(facts,51,'reason','evidence'),null);view.unmount();}
});
test('changed latest job or version during preview prevents confirmation',async()=>{
 for(const change of [()=>{management=base({service_version:4});},()=>{jobs.push(job({id:53,status:'failed'}));}]){reset();const view=await ready(),facts=await view.render().read(),standard=respond;respond=async call=>{if(call.url.endsWith('/status-correction-quotes')){const q=quote(JSON.parse(call.body));change();return q;}return standard(call);};assert.equal(await view.render().preview(facts,51,'reason','evidence'),null);assert.equal(writes().length,0);view.unmount();}
});
test('live role, actor, scope, ownership and late authority changes preserve isolation',async()=>{
 for(const change of [()=>{liveActor={...actor,id:8};},()=>{liveActor={...actor,customer_scope:'assigned'};},()=>{liveActor={...actor,permissions:['services.view','services.manage']};},()=>{management=base({owner_generation:2});}]){reset();const view=await ready(),input=await prepare(view);change();assert.equal(await view.render().run(input),null);assert.equal(writes().length,0);view.unmount();}
 reset();const view=await ready(),input=await prepare(view),standard=respond;respond=async call=>{if(call.headers){liveActor={...actor,id:8};return receipt(call);}return standard(call);};await view.render().run(input);assert.equal(storage.size,1);assert.equal(view.render().receipt,null);view.unmount();
});
test('definite refusal requires facts, while earlier unknown remains uncertain after conflict',async()=>{
 reset();let view=await ready(),input=await prepare(view);const standard=respond;respond=async call=>{if(call.headers)throw new ApiError(409,'service_status_source_invalid');return standard(call);};await view.render().run(input);assert.equal(view.render().pending.phase,'rejected');assert.equal(await view.render().run(input),null);assert.ok(await view.render().finishRejected());assert.equal(storage.size,0);view.unmount();
 reset();view=await ready();input=await prepare(view);respond=async call=>{if(call.headers)throw new TypeError('lost');return standard(call);};await view.render().run(input);respond=async call=>{if(call.headers)throw new ApiError(409,'stale_service_status_quote');return standard(call);};await view.render().run();assert.equal(view.render().pending.phase,'uncertain');assert.equal(await view.render().finishRejected(),null);assert.equal(storage.size,1);view.unmount();
});
test('invalid receipt and failed current facts after success retain original intent',async()=>{
 for(const override of [{service_id:32},{customer_id:42},{service_version:3},{source_job_id:52},{source_action:'resume'},{status:'active'},{before_status:'suspended'}]){reset();const view=await ready(),input=await prepare(view),standard=respond;respond=async call=>call.headers?{...receipt(call),...override}:standard(call);await view.render().run(input);assert.equal(view.render().pending.phase,'uncertain');assert.equal(storage.size,1);view.unmount();}
 reset();const view=await ready(),input=await prepare(view),standard=respond;let committed=false;respond=async call=>{if(call.headers){committed=true;return receipt(call);}if(committed&&call.url==='/admin/services/31')throw new ApiError(400,'read_failed');return standard(call);};await view.render().run(input);assert.equal(view.render().pending.phase,'uncertain');assert.equal(storage.size,1);view.unmount();
});
test('double click, unavailable storage, corrupt recovery and ownership mismatch cannot duplicate writes',async()=>{
 reset();let view=await ready(),input=await prepare(view);await Promise.all([view.render().run(input),view.render().run(input)]);assert.equal(writes().length,1);view.unmount();
 reset();view=await ready();input=await prepare(view);failStorage=true;await view.render().run(input);assert.equal(writes().length,0);view.unmount();
 for(const raw of ['{bad',JSON.stringify({body:{},quote:{},owner:99})]){reset();storage.set('goidc:service-status:admin:7:31',raw);view=await ready();assert.equal(view.render().blocked,true);assert.equal(await view.render().read(),null);assert.equal(writes().length,0);view.unmount();}
});
class Renderer {
  instances = new Map(); nodes = []; props = { service: { ...service }, refresh: () => refreshes++ };
  render() {
    this.nodes = []; const visited = new Set();
    const walk = (node, key = 'root') => {
      if (Array.isArray(node)) return node.map((child, index) => walk(child, `${key}/${child?.key ?? index}`));
      if (node === null || node === undefined || typeof node === 'boolean') return null;
      if (typeof node !== 'object') return String(node);
      if (typeof node.type === 'function') {
        const identity = `${key}:${node.type.name}:${node.key ?? ''}`; visited.add(identity); if (!this.instances.has(identity)) this.instances.set(identity, state());
        return walk(within(this.instances.get(identity), () => node.type(node.props)), `${identity}/result`);
      }
      const result = { type: node.type, props: node.props, children: walk(node.props?.children, `${key}/children`) }; this.nodes.push(result); return result;
    };
    this.tree = walk(React.createElement(AdminServiceStatusCorrection, this.props));
    for (const [key, value] of this.instances) if (!visited.has(key)) { value.values.forEach(item => item?.cleanup?.()); this.instances.delete(key); }
    return this;
  }
  text(node = this.tree) { return Array.isArray(node) ? node.map(value => this.text(value)).join('') : typeof node === 'string' ? node : node ? this.text(node.children) : ''; }
  button(text) { const found = this.nodes.find(node => node.type === 'button' && this.text(node) === text); assert.ok(found, text); return found.props; }
  label(label) { const found = this.nodes.find(node => node.props?.['aria-label'] === label); assert.ok(found, label); return found.props; }
  form(index) { return this.nodes.filter(node => node.type === 'form')[index].props; }
  unmount() { this.instances.forEach(value => value.values.forEach(item => item?.cleanup?.())); this.instances.clear(); }
}
async function readyRenderer() { const view = new Renderer().render(); await settle(); view.render(); return view; }

test('UI separates missing permission, unsupported state and local-only explicit confirmation',async()=>{
 reset();actor={...actor,permissions:['services.view','services.manage']};let view=new Renderer().render();assert.match(view.text(),/独立状态纠错权限/);assert.equal(calls.length,0);view.unmount();
 reset();view=await readyRenderer();assert.equal(view.nodes.some(n=>n.type==='button'&&view.text(n)==='预览状态纠错'),false);await view.button('读取当前状态与任务').onClick();view.render();await settle();view.render(); // event uses void callback; wait for fetches
 assert.equal(view.label('纠错来源任务 ID').value,'51');view.label('状态纠错原因').onChange({target:{value:'reason'}});view.render();view.label('完成凭证及核对说明').onChange({target:{value:'evidence'}});view.render();view.button('预览状态纠错').onClick();await settle();view.render();assert.equal(view.button('确认执行状态纠错').disabled,true);view.label('确认仅修正本地状态').onCheckedChange(true);view.render();assert.equal(view.button('确认执行状态纠错').disabled,false);view.label('状态纠错原因').onChange({target:{value:'new reason'}});view.render();assert.equal(view.nodes.some(n=>n.type==='button'&&view.text(n)==='确认执行状态纠错'),false);assert.equal(writes().length,0);view.unmount();
});
test('UI expires preview visibly and retains recovery for a later terminal/mode change',async()=>{
 reset();let view=await readyRenderer();view.button('读取当前状态与任务').onClick();await settle();view.render();view.label('状态纠错原因').onChange({target:{value:'reason'}});view.render();view.label('完成凭证及核对说明').onChange({target:{value:'evidence'}});view.render();const standard=respond;respond=async call=>call.url.endsWith('/status-correction-quotes')?{...quote(JSON.parse(call.body)),expires_at:new Date(Date.now()+80).toISOString()}:standard(call);view.button('预览状态纠错').onClick();await settle();view.render();await new Promise(resolve=>setTimeout(resolve,100));view.render();assert.match(view.text(),/预览已过期/);assert.equal(view.button('确认执行状态纠错').disabled,true);view.unmount();
 reset();const mutation=await ready(),input=await prepare(mutation),normal=respond;respond=async call=>{if(call.headers)throw new TypeError('lost');return normal(call);};await mutation.render().run(input);mutation.unmount();management=base({status:'terminated',service_version:8});mode='supplier';view=new Renderer();view.props.service={...service,status:'terminated',version:8,provision_mode:'supplier'};view.render();await settle();view.render();assert.equal(view.button('恢复原状态纠错请求').disabled,false);assert.equal(view.button('读取当前状态与任务').disabled,true);view.unmount();
});
