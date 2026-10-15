"use client";
import { useEffect } from "react";
import Link from "next/link";
import { Home, RotateCcw, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { SiteBrand, SiteFooter } from "@/components/site-provider";

export default function ErrorPage({ error, reset }: { error: Error & { digest?: string }; reset: () => void }) {
  useEffect(() => {
    console.error("[zjmfgo] page error:", error);
  }, [error]);
  return (
    <main className="min-h-screen bg-background flex flex-col">
      <header className="w-full max-w-4xl mx-auto px-6 py-8">
        <Link href="/" className="inline-flex min-w-0 max-w-full">
          <SiteBrand />
        </Link>
      </header>
      <section className="w-full max-w-4xl mx-auto px-6 py-16 flex-1">
        <TriangleAlert className="text-destructive mb-6" size={32} aria-hidden="true" />
        <h1 className="page-title">页面暂时无法加载</h1>
        <p className="mt-4 max-w-2xl leading-7 text-muted-foreground break-words">
          页面在渲染时遇到错误，可以重试；如果反复出现，请把下面的错误编号反馈给站点支持。
        </p>
        <p role="alert" className="mt-4 max-w-2xl break-words text-sm text-destructive">
          {error.message || "未知错误"}
          {error.digest ? <span className="ml-2 font-mono text-xs text-muted-foreground">错误编号：{error.digest}</span> : null}
        </p>
        <div className="flex flex-wrap gap-3 mt-8">
          <Button onClick={reset}>
            <RotateCcw size={16} />
            重新加载
          </Button>
          <Button variant="outline" asChild>
            <Link href="/">
              <Home size={16} />
              返回首页
            </Link>
          </Button>
        </div>
      </section>
      <div className="border-t w-full max-w-4xl mx-auto px-6 py-6">
        <SiteFooter />
      </div>
    </main>
  );
}
