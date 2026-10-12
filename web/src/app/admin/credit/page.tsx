"use client";
import { useEffect, useState } from "react";
import { api, money } from "@/lib/api";

type Row = { id:number; username:string; display_name:string; email:string; status:string; limit_cents:number; used_cents:number; available_cents:number; version:number };
type Page = { items:Row[]; total:number; limit:number; offset:number };

export default function Page() {
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [data, setData] = useState<Page>({items:[], total:0, limit:20, offset:0});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [offset,setOffset]=useState(0);
  const [canManage,setCanManage]=useState(false);
  const load = async () => { setError(""); try { setData(await api<Page>(`/admin/credit?q=${encodeURIComponent(query)}&status=${encodeURIComponent(status)}&limit=20&offset=${offset}`)); } catch (e) { setError((e as Error).message); } };
  useEffect(() => { void load(); }, [offset]);
  useEffect(()=>{void api<{role:string;permissions:string[]}>("/me").then(u=>setCanManage(u.role==="super_admin"||u.permissions.includes("credit.manage")));},[]);
  const setLimit = async (row:Row) => {
    const raw = window.prompt(`设置 ${row.display_name || row.username} 的授权额度（分）`, String(row.limit_cents));
    if (raw === null) return;
    const limit = Number(raw);
    if (!Number.isSafeInteger(limit) || limit < row.used_cents) { setError("额度必须是不小于已用额度的整数（分）"); return; }
    setBusy(true); setError("");
    try { await api(`/admin/credit/${row.id}/limit`, {method:"PUT", headers:{"Idempotency-Key":`credit-limit-${row.id}-${row.version}-${crypto.randomUUID()}`}, body:JSON.stringify({limit_cents:limit, expected_version:row.version})}); await load(); }
    catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };
  return <main className="space-y-5"><div><h1 className="text-2xl font-semibold">信用额管理</h1><p className="text-sm text-muted-foreground">显示客户授权额度、已用额度和可用额度；调整只修改授信账户，不改变客户余额。</p></div><form className="flex flex-wrap gap-2" onSubmit={e=>{e.preventDefault();void load();}}><input className="border rounded px-3 py-2" value={query} onChange={e=>setQuery(e.target.value)} placeholder="请搜索客户"/><select className="border rounded px-3 py-2" value={status} onChange={e=>setStatus(e.target.value)}><option value="">全部状态</option><option value="active">启用</option><option value="disabled">停用</option></select><button className="border rounded px-3 py-2" type="submit">搜索</button></form>{error&&<p role="alert" className="text-sm text-destructive">{error}</p>}<div className="overflow-auto border rounded"><table className="w-full text-sm"><thead><tr className="border-b text-left"><th className="p-3">客户</th><th className="p-3">状态</th><th className="p-3">授权额度</th><th className="p-3">已用额度</th><th className="p-3">可用额度</th><th className="p-3">操作</th></tr></thead><tbody>{data.items.map(row=><tr className="border-b" key={row.id}><td className="p-3">{row.display_name || row.username}<br/><span className="text-muted-foreground">{row.email}</span></td><td className="p-3">{row.status}</td><td className="p-3">{money(row.limit_cents)}</td><td className="p-3">{money(row.used_cents)}</td><td className="p-3">{money(row.available_cents)}</td><td className="p-3"><button className="underline" disabled={busy||!canManage} onClick={()=>void setLimit(row)}>设置额度</button></td></tr>)}{data.items.length===0&&<tr><td className="p-6 text-center text-muted-foreground" colSpan={6}>暂无数据</td></tr>}</tbody></table></div><p className="text-xs text-muted-foreground">共 {data.total} 条</p><div className="flex gap-3"><button disabled={offset===0} onClick={()=>setOffset(Math.max(0,offset-20))}>上一页</button><button disabled={offset+20>=data.total} onClick={()=>setOffset(offset+20)}>下一页</button></div></main>;
}
