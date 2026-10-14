"use client";
import { useEffect } from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { RefreshCw, Shield, Wrench } from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import type { User } from "@/lib/api";
import { Button } from "./ui/button";
import { SiteBrand, SiteFooter, useSite } from "./site-provider";
import { useUnauthorizedRedirect } from "@/hooks/use-unauthorized-redirect";

export function SiteAvailability({ children }: { children: React.ReactNode }) {
  const { site } = useSite();
  const path = usePathname();
  useUnauthorizedRedirect();
  const exempt = path === "/admin" || path.startsWith("/admin/") || ["/admin-login", "/maintenance", "/forgot-password", "/reset-password"].includes(path);
  const { data: user, error, refresh: refreshUser } = useResource<User>(site?.maintenance_enabled && !exempt ? "/me" : null, false);
  useEffect(() => {
    if (site?.maintenance_enabled && !exempt) refreshUser();
  }, [site, exempt, refreshUser]);
  const blocked = site?.maintenance_enabled && !exempt && user?.role !== "admin" && user?.role !== "super_admin";
  useEffect(() => {
    if (blocked && (user || error) && site?.maintenance_url) window.location.replace(site.maintenance_url);
  }, [blocked, user, error, site?.maintenance_url]);
  return blocked ? <MaintenanceScreen /> : children;
}

export function MaintenanceScreen() {
  const { site, refresh } = useSite();
  const router = useRouter();
  const path = usePathname();
  useEffect(() => {
    if (site && !site.maintenance_enabled) {
      if (path === "/maintenance") router.replace("/");
      else router.refresh();
    }
  }, [path, site, router]);
  return <main className="min-h-screen bg-white flex flex-col"><header className="w-full max-w-4xl mx-auto px-6 py-8"><SiteBrand /></header><section className="w-full max-w-4xl mx-auto px-6 py-16 flex-1"><Wrench className="text-primary mb-6" size={32} /><h1 className="text-2xl font-semibold mb-5">站点维护中</h1><p role="status" className="text-muted-foreground whitespace-pre-wrap break-words max-w-2xl leading-7">{site?.maintenance_message || "站点暂时不可用，请稍后再试。"}</p><div className="flex flex-wrap gap-3 mt-8"><Button variant="outline" onClick={() => { refresh(); router.refresh(); }}><RefreshCw />重新检查</Button><Button variant="ghost" asChild><Link href="/admin-login"><Shield />管理员登录</Link></Button></div></section><div className="border-t w-full max-w-4xl mx-auto px-6 py-6"><SiteFooter /></div></main>;
}
