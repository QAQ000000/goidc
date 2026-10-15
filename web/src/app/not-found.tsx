import type { Metadata } from "next";
import Link from "next/link";
import { Compass, Download, Home, ShieldCheck } from "lucide-react";
import { SiteBrand, SiteFooter } from "@/components/site-provider";
import { Button } from "@/components/ui/button";

export const metadata: Metadata = { title: "页面不存在", robots: { index: false, follow: false } };

export default function NotFound() {
  return (
    <main className="min-h-screen bg-background flex flex-col">
      <header className="w-full max-w-4xl mx-auto px-6 py-8">
        <Link href="/" className="inline-flex min-w-0 max-w-full">
          <SiteBrand />
        </Link>
      </header>
      <section className="w-full max-w-4xl mx-auto px-6 py-16 flex-1">
        <p className="text-sm font-medium text-muted-foreground">404</p>
        <h1 className="page-title mt-3">页面不存在或已失效</h1>
        <p role="status" className="mt-4 max-w-2xl leading-7 text-muted-foreground break-words">
          你访问的链接可能已经过期、被移动，或者没有对应的内容。可以返回首页继续浏览，或登录账户后查看订单与服务。
        </p>
        <div className="flex flex-wrap gap-3 mt-8">
          <Button asChild>
            <Link href="/">
              <Home size={16} />
              返回首页
            </Link>
          </Button>
          <Button variant="outline" asChild>
            <Link href="/downloads">
              <Download size={16} />
              下载中心
            </Link>
          </Button>
          <Button variant="outline" asChild>
            <Link href="/login">
              <ShieldCheck size={16} />
              登录账户
            </Link>
          </Button>
        </div>
        <p className="mt-10 flex items-center gap-2 text-xs text-muted-foreground">
          <Compass size={14} aria-hidden="true" />
          如果确认链接无误仍然打不开，请联系站点支持。
        </p>
      </section>
      <div className="border-t w-full max-w-4xl mx-auto px-6 py-6">
        <SiteFooter />
      </div>
    </main>
  );
}
