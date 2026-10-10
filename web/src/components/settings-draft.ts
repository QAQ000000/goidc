"use client";
import { useEffect, useState } from "react";
import { type SiteSettings, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";

export function useSettingsDraft(form: SiteSettings, baseline: SiteSettings, manage: boolean) {
  const user = useResource<User>("/me", false);
  const key = user.data ? `zjmfgo:settings:draft:${user.data.id}` : null;
  const dirty = JSON.stringify(form) !== JSON.stringify(baseline);
  const [draft, setDraft] = useState<{ baseVersion: number; form: SiteSettings } | null>(null);
  const [loaded, setLoaded] = useState<string | null>(null);
  useEffect(() => {
    if (!key || !manage) return;
    const timer = window.setTimeout(() => {
    try {
      const raw = sessionStorage.getItem(key);
      const value = raw ? JSON.parse(raw) : null;
      if (value && Number.isInteger(value.baseVersion) && value.form && typeof value.form.site_name === "string") setDraft(value);
    } catch { /* Session storage is optional. */ }
    setLoaded(key);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [key, manage]);
  useEffect(() => {
    if (!key || loaded !== key || !manage || !dirty) return;
    const safe = Object.fromEntries(Object.keys(baseline).map(k => [k, form[k as keyof SiteSettings]]));
    safe.admin_settings = Object.fromEntries(Object.keys(baseline.admin_settings).map(k => [k, form.admin_settings[k as keyof SiteSettings["admin_settings"]]])) as SiteSettings["admin_settings"];
    try { sessionStorage.setItem(key, JSON.stringify({ baseVersion: baseline.version, form: safe })); } catch {}
  }, [key, loaded, manage, dirty, form, baseline]);
  useEffect(() => {
    if (!dirty) return;
    const unload = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = ""; };
    const click = (e: MouseEvent) => {
      const anchor = e.target instanceof Element ? e.target.closest("a[href]") : null;
      if (anchor && !e.ctrlKey && !e.metaKey && !e.shiftKey && !window.confirm("修改尚未保存，是否离开？会话草稿将保留。")) { e.preventDefault(); e.stopPropagation(); }
    };
    window.addEventListener("beforeunload", unload); document.addEventListener("click", click, true);
    return () => { window.removeEventListener("beforeunload", unload); document.removeEventListener("click", click, true); };
  }, [dirty]);
  const clear = () => { if (key) { try { sessionStorage.removeItem(key); } catch {} } setDraft(null); };
  const recover = () => {
    if (!draft || (draft.baseVersion !== baseline.version && !window.confirm("草稿版本与当前设置不同，确认恢复并逐项检查？"))) return null;
    const safe = Object.fromEntries(Object.keys(baseline).map(k => [k, draft.form[k as keyof SiteSettings] ?? baseline[k as keyof SiteSettings]])) as SiteSettings;
    safe.admin_settings = Object.fromEntries(Object.keys(baseline.admin_settings).map(k => [k, draft.form.admin_settings?.[k as keyof SiteSettings["admin_settings"]] ?? baseline.admin_settings[k as keyof SiteSettings["admin_settings"]]])) as SiteSettings["admin_settings"];
    // Remove the persisted draft before returning the recovered form. The form
    // becomes dirty immediately, so leaving the entry in sessionStorage would
    // persist the recovered values again on the next effect pass.
    if (key) { try { sessionStorage.removeItem(key); } catch {} }
    setDraft(null);
    return { ...safe, version: baseline.version, updated_at: baseline.updated_at };
  };
  return { draft, clear, recover };
}
