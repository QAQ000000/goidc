"use client";
import Link from "next/link";
import { ArrowLeft, BookOpen, Newspaper, Search } from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ApiError, date, type ContentArticle, type ContentCategory, type ContentTag, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { SiteBrand, SiteFooter, useSite } from "./site-provider";

export function PublicShell({ children, title }: { children: React.ReactNode; title: string }) {
  const { site } = useSite();
  const identity = useResource<User>("/me", false);
  const staff = identity.data?.role === "admin" || identity.data?.role === "super_admin";
  return <main className="min-h-screen bg-background"><header className="border-b bg-white"><div className="max-w-5xl mx-auto px-5 py-5 flex flex-wrap gap-4 items-center justify-between"><Link href="/" className="min-w-0 max-w-full"><SiteBrand compact /></Link><nav className="flex flex-wrap items-center gap-4 text-sm"><Link href="/news" className="text-muted-foreground hover:text-foreground">新闻</Link><Link href="/knowledgebase" className="text-muted-foreground hover:text-foreground">知识库</Link><Link href="/downloads" className="text-muted-foreground hover:text-foreground">下载</Link><Link href={staff ? "/admin" : identity.data?.role === "customer" ? "/console/orders" : "/login"} className="text-primary">{staff ? "返回管理后台" : identity.data?.role === "customer" ? "客户中心" : "登录"}</Link></nav></div></header><div className="max-w-5xl mx-auto px-5 py-10"><p className="text-sm text-muted-foreground mb-2 break-words">{site?.site_name} · {title}</p>{children}</div><div className="border-t px-5 py-6 max-w-5xl mx-auto"><SiteFooter /></div></main>;
}

export function ContentList({ type }: { type: "news" | "knowledge" }) {
  const title = type === "news" ? "新闻中心" : "知识库";
  const Icon = type === "news" ? Newspaper : BookOpen;
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState("");
  const [category, setCategory] = useState("");
  const [tag, setTag] = useState("");
  const [offset, setOffset] = useState(0);
  const categories = useResource<Pick<ContentCategory, "id" | "name">[]>(`/content-categories?content_type=${type}`, false);
  const tags = useResource<ContentTag[]>(`/content-tags?content_type=${type}`, false);
  const { data, error, refresh } = useResource<{ items: ContentArticle[]; total: number }>(`/${type === "news" ? "news" : "knowledgebase"}?q=${encodeURIComponent(query)}&category_id=${category}&tag_id=${tag}&limit=20&offset=${offset}`, false);
  return <PublicShell title={title}>
    <div className="mb-8"><div className="flex items-center gap-3"><Icon className="text-primary" size={23} /><h1 className="text-3xl font-semibold">{title}</h1></div><p className="text-muted-foreground mt-3">查找最新信息与常见问题解答</p></div>
    <form className="grid sm:grid-cols-[1fr_1fr_2fr_auto] gap-3 mb-6" onSubmit={event => { event.preventDefault(); setQuery(search); setOffset(0); }}>
      <select aria-label="内容分类" className="h-9 min-w-0 rounded-md border border-input bg-white px-3 text-sm" value={category} onChange={event => { setCategory(event.target.value); setOffset(0); }}><option value="">全部分类</option><option value="0">未分类</option>{categories.data?.map(c => <option key={c.id} value={c.id}>{c.name}</option>)}</select>
      <select aria-label="内容标签" className="h-9 min-w-0 rounded-md border border-input bg-white px-3 text-sm" value={tag} onChange={event => { setTag(event.target.value); setOffset(0); }}><option value="">全部标签</option>{tags.data?.map(t => <option key={t.id} value={t.id}>{t.name}</option>)}</select>
      <Input aria-label="搜索内容" placeholder="搜索标题或正文" value={search} onChange={event => setSearch(event.target.value)} />
      <Button variant="outline"><Search size={16} />搜索</Button>
    </form>
    {categories.error && <p role="alert" className="text-sm text-destructive mb-4">分类加载失败<Button variant="link" onClick={categories.refresh}>重试</Button></p>}
    {tags.error && <p role="alert" className="text-sm text-destructive mb-4">标签加载失败<Button variant="link" onClick={tags.refresh}>重试</Button></p>}
    {type === "knowledge" && <p className="text-sm text-muted-foreground mb-5">部分文章仅对登录客户或拥有有效服务的客户开放。<Link href="/login" className="text-primary ml-1">登录账户</Link></p>}
    {error ? <div className="border border-red-200 bg-red-50 text-destructive p-4 rounded-md" role="alert">{error.message}<Button variant="link" onClick={refresh}>重试</Button></div>
      : !data ? <p className="text-muted-foreground">加载中...</p>
      : <>{data.items.length === 0 ? <p className="py-20 text-center text-muted-foreground">暂无符合条件的内容</p>
        : <div className="divide-y border-y">{data.items.map(article => <Link key={article.id} href={`/${type === "news" ? "news" : "knowledgebase"}/${article.id}`} className="block py-6 hover:bg-muted/40 px-3">
          <div className="flex flex-wrap items-start justify-between gap-3"><h2 className="font-medium text-lg min-w-0 break-all">{article.title}</h2><time className="text-xs text-muted-foreground whitespace-nowrap">{article.published_at ? date(article.published_at) : ""}</time></div>
          <p className="text-xs text-muted-foreground mt-2">{article.categories.map(c => c.name).join(" · ")}{article.tags.length ? ` · ${article.tags.map(t => t.name).join(" · ")}` : ""}</p>
          <p className="text-sm text-muted-foreground mt-2 line-clamp-2 break-all">{article.excerpt || article.body}</p>
        </Link>)}</div>}
        <div className="flex flex-wrap justify-between items-center gap-3 mt-5 text-sm"><span>{data.total} 篇文章</span><div className="flex gap-2"><Button variant="outline" disabled={offset === 0} onClick={() => setOffset(v => Math.max(0, v - 20))}>上一页</Button><Button variant="outline" disabled={offset + 20 >= data.total} onClick={() => setOffset(v => v + 20)}>下一页</Button></div></div>
      </>}
  </PublicShell>;
}

export function ContentDetail({ type, id, initialArticle }: { type: "news" | "knowledge"; id: number; initialArticle?: ContentArticle }) {
  const title = type === "news" ? "新闻详情" : "知识库文章";
  const { data, error, refresh } = useResource<ContentArticle>(`/${type === "news" ? "news" : "knowledgebase"}/${id}`, false);
  const article = error ? undefined : data ?? initialArticle;
  return <PublicShell title={title}>
    <Link href={`/${type === "news" ? "news" : "knowledgebase"}`} className="inline-flex items-center gap-2 text-sm text-primary mb-8"><ArrowLeft size={15} />返回列表</Link>
    {error ? <div className="border border-red-200 bg-red-50 text-destructive p-4 rounded-md" role="alert">
      {error instanceof ApiError && error.status === 404 ? "文章不存在或当前账户无法访问。" : error.message}
      <Button variant="link" onClick={refresh}>重试</Button>{type === "knowledge" && <Link href="/login" className="text-primary ml-3">登录账户</Link>}
    </div> : !article ? <p className="text-muted-foreground">加载中...</p>
      : <article className="max-w-3xl"><h1 className="text-3xl font-semibold leading-tight break-all">{article.title}</h1><div className="text-xs text-muted-foreground mt-3 break-words">{article.categories.map(c => c.name).join(" · ") || "未分类"} · {article.published_at ? date(article.published_at) : ""}</div>{article.tags.length > 0 && <div className="flex flex-wrap gap-2 mt-4">{article.tags.map(t => <span key={t.id} className="border rounded px-2 py-1 text-xs text-muted-foreground">{t.name}</span>)}</div>}{article.body_html ? <div className="mt-10 leading-8 text-[15px] break-words [&_img]:max-w-full [&_a]:text-primary [&_a]:underline" dangerouslySetInnerHTML={{ __html: article.body_html }} /> : <div className="mt-10 whitespace-pre-wrap break-all leading-8 text-[15px]">{article.body}</div>}</article>}

  </PublicShell>;
}
