"use client";
import { createContext, useContext, useEffect } from "react";
import Image from "next/image";
import type { SiteProfile } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";

const SiteContext = createContext<{ site: SiteProfile | null; error: Error | null; refresh: () => void }>({ site: null, error: null, refresh: () => {} });
export function SiteProvider({ initial, children }: { initial: SiteProfile | null; children: React.ReactNode }) {
  const { data, error, refresh } = useResource<SiteProfile>("/site", false);
  useEffect(() => {
    const update = () => { if (document.visibilityState === "visible") refresh(); };
    window.addEventListener("focus", update);
    document.addEventListener("visibilitychange", update);
    window.addEventListener("zjmfgo:maintenance", update);
    const timer = window.setInterval(update, 30000);
    return () => { window.clearInterval(timer); window.removeEventListener("focus", update); window.removeEventListener("zjmfgo:maintenance", update); document.removeEventListener("visibilitychange", update); };
  }, [refresh]);
  return <SiteContext.Provider value={{ site: data ?? initial, error, refresh }}>{children}</SiteContext.Provider>;
}
export function useSite() { return useContext(SiteContext); }
export function SiteBrand({ compact = false }: { compact?: boolean }) {
  const { site } = useSite();
  const name = site?.site_name ?? "智简魔方";
  return <span className="flex flex-col gap-2 min-w-0 max-w-full">{site?.logo_url && <span className="relative block h-8 w-36 max-w-full"><Image src={site.logo_url} alt={name} fill unoptimized loading="eager" sizes="144px" className="object-contain object-left" /></span>}<span className={`${compact ? "text-sm" : "text-lg"} font-semibold whitespace-normal break-words`}>{name}</span></span>;
}
export function SiteFooter() {
  const { site } = useSite();
  if (!site) return null;
  return <footer className="text-xs text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-2 leading-relaxed break-words">
    {site.company_name && <span>{site.company_name}</span>}
    {site.copyright && <span>{site.copyright}</span>}
    {site.record_number && <a href="https://beian.miit.gov.cn/" target="_blank" rel="noopener noreferrer">{site.record_number}</a>}
    {site.terms_url && <a href={site.terms_url} className="underline underline-offset-4">服务条款</a>}
    {site.privacy_url && <a href={site.privacy_url} className="underline underline-offset-4">隐私政策</a>}
    {site.support_url && <a href={site.support_url}>联系支持</a>}
    {site.support_email && <a className="break-all" href={`mailto:${site.support_email}`}>{site.support_email}</a>}
    {site.support_phone && <span>{site.support_phone}</span>}
    {site.address && <span>{site.address}</span>}
  </footer>;
}
