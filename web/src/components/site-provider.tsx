"use client";
import { createContext, useContext, useEffect } from "react";
import Image from "next/image";
import { usePathname } from "next/navigation";
import type { PublicSite, SiteTheme } from "@/lib/api";
import { defaultSiteTheme, normalizeSiteTheme, siteThemeApplies, siteThemePortalCSS, siteThemeTokens } from "@/lib/site-theme";
import { useResource } from "@/hooks/use-resource";

const SiteContext = createContext<{ site: PublicSite | null; theme: SiteTheme; error: Error | null; refresh: () => void }>({ site: null, theme: { ...defaultSiteTheme }, error: null, refresh: () => {} });
export function SiteProvider({ initial, children }: { initial: PublicSite | null; children: React.ReactNode }) {
  const pathname = usePathname();
  const themed = siteThemeApplies(pathname);
  const { data, error, refresh } = useResource<PublicSite>("/site", true);
  const site = data ?? initial;
  const theme = normalizeSiteTheme(themed ? site?.theme : null);
  useEffect(() => {
    const update = () => { if (document.visibilityState === "visible") refresh(); };
    window.addEventListener("focus", update);
    document.addEventListener("visibilitychange", update);
    window.addEventListener("zjmfgo:maintenance", update);
    const timer = window.setInterval(update, 30000);
    return () => { window.clearInterval(timer); window.removeEventListener("focus", update); window.removeEventListener("zjmfgo:maintenance", update); document.removeEventListener("visibilitychange", update); };
  }, [refresh]);
  return <SiteContext.Provider value={{ site, theme, error, refresh }}>{themed ? <div className="site-theme-scope" data-site-template={theme.template} style={siteThemeTokens(theme)}><style>{siteThemePortalCSS(theme)}</style>{children}</div> : children}</SiteContext.Provider>;
}
export function useSite() { return useContext(SiteContext); }
export function SiteBrand({ compact = false }: { compact?: boolean }) {
  const { site, theme } = useSite();
  const name = site?.site_name ?? "智简魔方";
  const tight = theme.logo_variant === "compact";
  return <span data-logo-variant={theme.logo_variant} className={`flex ${tight ? "flex-row items-center" : "flex-col"} gap-2 min-w-0 max-w-full`}>{site?.logo_url && theme.logo_variant !== "text" && <span className={`relative block shrink-0 ${tight ? "h-6 w-6" : "h-8 w-36"} max-w-full`}><Image src={site.logo_url} alt={name} fill unoptimized loading="eager" sizes={tight ? "24px" : "144px"} className="object-contain object-left" /></span>}<span className={`${compact ? "text-sm" : "text-lg"} font-semibold whitespace-normal break-words`}>{name}</span></span>;
}
export function SiteFooter() {
  const { site, theme } = useSite();
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
    {theme.footer_text && <span className="basis-full min-w-0 max-w-full whitespace-pre-wrap [overflow-wrap:anywhere]">{theme.footer_text}</span>}
  </footer>;
}
