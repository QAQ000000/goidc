"use client";
import Link from "next/link";
import { useState } from "react";
import { Download, ExternalLink, LockKeyhole, Search } from "lucide-react";
import { ApiError, date, fetchDownload, type DownloadItem, type DownloadCategory } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { PublicShell } from "./public-content";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Failure, Loading } from "./common";

export function categoryLabel(category: Pick<DownloadCategory,"name"|"path">) { return category.path?.join(" / ") || category.name; }
export const downloadAccessNames = { public:"公开下载",login:"登录后下载",product:"指定产品客户" };
export function fileSize(bytes:number) { return bytes < 1024 ? `${bytes} B` : bytes >= 1048576 ? `${(bytes/1048576).toFixed(1)} MiB` : `${(bytes/1024).toFixed(1)} KiB`; }
export function DownloadsView() {
 const [query,setQuery]=useState(""); const [search,setSearch]=useState(""); const [category,setCategory]=useState(""); const [offset,setOffset]=useState(0);
 const [busy,setBusy]=useState<number|null>(null);const [error,setError]=useState("");const [login,setLogin]=useState(false);
 const categories=useResource<Pick<DownloadCategory,"id"|"name"|"path"|"file_count">[]>("/download-categories",false);
 const files=useResource<{ enabled:boolean;items:DownloadItem[];total:number }>(`/downloads?q=${encodeURIComponent(query)}&category_id=${category}&offset=${offset}&limit=20`,false);
 async function start(item:DownloadItem) {
  if(busy!==null)return;setBusy(item.id);setError("");setLogin(false);
  try{await fetchDownload(item);files.refresh();}catch(e){setError((e as Error).message);setLogin(e instanceof ApiError && e.status===401);}finally{setBusy(null);}
 }
 return <PublicShell title="下载中心"><div className="mb-8"><div className="flex gap-3 items-center"><Download className="text-primary" size={24}/><h1 className="text-3xl font-semibold">下载中心</h1></div><p className="mt-3 text-muted-foreground">产品工具、文档与使用资源</p><p className="mt-2 text-sm text-muted-foreground">部分资源需要登录，或持有指定产品的有效服务。<Link href="/login" className="ml-2 text-primary">登录账户</Link><Link href="/console/services" className="ml-3 text-primary">我的服务</Link></p></div>
 <form className="grid sm:grid-cols-[1fr_2fr_auto] gap-3 mb-6" onSubmit={e=>{e.preventDefault();setQuery(search);setOffset(0);}}><select aria-label="下载分类" value={category} onChange={e=>{setCategory(e.target.value);setOffset(0);}} className="h-9 min-w-0 rounded-md border bg-white px-3 text-sm"><option value="">全部分类</option><option value="0">未分类</option>{categories.data?.map(c=><option key={c.id} value={c.id}>{categoryLabel(c)}（{c.file_count}）</option>)}</select><Input aria-label="搜索下载" placeholder="搜索文件名称或说明" value={search} onChange={e=>setSearch(e.target.value)}/><Button variant="outline"><Search size={16}/>搜索</Button></form>
 {categories.error && <Failure error={categories.error} retry={categories.refresh}/>}
 {error && <p role="alert" className="mb-5 text-sm text-destructive">{error}{login && <Link href="/login" className="ml-3 underline">登录账户</Link>}</p>}
 {files.error ? <Failure error={files.error} retry={files.refresh}/> : !files.data ? <Loading/> : !files.data.enabled ? <div className="rounded-xl border bg-muted/40 p-10 text-center">下载中心暂时关闭</div> : <><div className="divide-y border-y">{files.data.items.map(item=><article key={item.id} className="py-6 grid sm:grid-cols-[1fr_auto] gap-4"><div className="min-w-0"><h2 className="text-lg font-medium break-words">{item.title}</h2><p className="mt-2 whitespace-pre-wrap text-sm text-muted-foreground break-words">{item.description}</p><p className="mt-3 text-xs text-muted-foreground break-words">{item.category_path?.join(" / ") || item.category_name || "未分类"} · {downloadAccessNames[item.access_level]} · {item.source_type==="remote" ? "外部链接" : `${item.filename} · ${fileSize(item.size_bytes)}`} · {item.downloads} 次下载</p><p className="mt-1 text-xs text-muted-foreground">更新于 {date(item.updated_at)}</p>{item.sha256 && <details className="mt-2 text-xs text-muted-foreground"><summary className="cursor-pointer">文件校验值 SHA-256</summary><code className="block mt-2 break-all">{item.sha256}</code></details>}</div><div className="flex items-start"><Button variant={item.can_download ? "default" : "outline"} disabled={busy!==null} aria-label={`下载 ${item.title}`} onClick={()=>void start(item)}>{busy===item.id ? "处理中…" : <>{!item.can_download ? <LockKeyhole size={16}/> : item.source_type==="remote" ? <ExternalLink size={16}/> : <Download size={16}/>} {item.source_type==="remote" ? "访问外部下载" : "下载文件"}</>}</Button></div></article>)}{files.data.items.length===0 && <p className="py-20 text-center text-muted-foreground">暂无符合条件的文件</p>}</div><div className="mt-5 flex flex-wrap justify-between gap-3 text-sm"><span>{files.data.total} 个文件</span><div className="flex gap-2"><Button variant="outline" disabled={offset===0} onClick={()=>setOffset(v=>Math.max(0,v-20))}>上一页</Button><Button variant="outline" disabled={offset+20>=files.data.total} onClick={()=>setOffset(v=>v+20)}>下一页</Button></div></div></>}
 </PublicShell>;
}
