"use client";

import Link from "next/link";
import { useState } from "react";
import { Download, ExternalLink, LockKeyhole } from "lucide-react";
import { fetchDownload, type DownloadItem } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Failure, Loading } from "./common";
import { fileSize } from "./downloads";

export function ServiceDownloads({ serviceID }: { serviceID: number }) {
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [offset, setOffset] = useState(0);
  const [busy, setBusy] = useState<number | null>(null);
  const [error, setError] = useState("");
  const root = `/services/${serviceID}/downloads`;
  const files = useResource<{ enabled: boolean; items: DownloadItem[]; total: number }>(`${root}?q=${encodeURIComponent(query)}&offset=${offset}&limit=20`, false);

  async function download(file: DownloadItem) {
    if (busy !== null) return;
    setBusy(file.id); setError("");
    try {
      await fetchDownload(file, `${root}/${file.id}/file`);
      files.refresh();
    } catch (e) {
      setError((e as Error).message);
      files.refresh();
    } finally { setBusy(null); }
  }

  return <section className="data-surface p-6 mb-8" aria-label="产品资料">
    <div className="flex flex-wrap justify-between gap-3 mb-5">
      <div><h2 className="font-semibold">产品资料</h2><p className="text-xs text-muted-foreground mt-1">此服务产品关联的工具与文档。受限文件需要持有有效服务。</p></div>
      <Link href="/downloads" className="text-sm text-primary">全部下载</Link>
    </div>
    <form className="flex gap-3 mb-5" onSubmit={e => { e.preventDefault(); setQuery(search); setOffset(0); }}>
      <Input aria-label="搜索产品资料" placeholder="搜索文件名称或说明" value={search} onChange={e => setSearch(e.target.value)} />
      <Button variant="outline">搜索</Button>
    </form>
    {error && <p role="alert" className="text-sm text-destructive mb-4">{error}</p>}
    {files.error ? <Failure error={files.error} retry={files.refresh} /> : !files.data ? <Loading /> : !files.data.enabled ? <p className="py-6 text-center text-sm text-muted-foreground">文件下载暂时关闭</p> : <>
      <div className="divide-y">{files.data.items.map(file => <article key={file.id} className="py-4 flex flex-wrap gap-4 items-start">
        <div className="min-w-0 flex-1"><h3 className="font-medium break-words">{file.title}</h3><p className="text-sm text-muted-foreground whitespace-pre-wrap break-words mt-2">{file.description}</p><p className="text-xs text-muted-foreground break-words mt-2">{file.source_type === "remote" ? "外部链接" : `${file.filename} · ${fileSize(file.size_bytes)}`} · {file.downloads} 次下载</p>{!file.can_download && <p className="text-xs text-muted-foreground mt-2">需要持有指定产品的有效服务</p>}{file.sha256 && <details className="text-xs text-muted-foreground mt-2"><summary className="cursor-pointer">SHA-256 校验值</summary><code className="block break-all mt-2">{file.sha256}</code></details>}</div>
        <Button variant={file.can_download ? "default" : "outline"} disabled={busy !== null} aria-label={`下载产品资料 ${file.title}`} onClick={() => void download(file)}>{!file.can_download ? <LockKeyhole size={16} /> : file.source_type === "remote" ? <ExternalLink size={16} /> : <Download size={16} />}{busy === file.id ? "处理中…" : file.source_type === "remote" ? "访问外部下载" : "下载文件"}</Button>
      </article>)}{files.data.items.length === 0 && <p className="py-8 text-center text-sm text-muted-foreground">暂无符合条件的产品资料</p>}</div>
      <div className="flex flex-wrap justify-between gap-3 mt-4 text-sm"><span>{files.data.total} 个文件</span><div className="flex gap-2"><Button variant="outline" disabled={offset === 0} onClick={() => setOffset(v => Math.max(0, v - 20))}>上一页</Button><Button variant="outline" disabled={offset + 20 >= files.data.total} onClick={() => setOffset(v => v + 20)}>下一页</Button></div></div>
    </>}
  </section>;
}
