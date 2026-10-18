import "server-only";
import { cache } from "react";
import type { ContentArticle, Product, PublicSite } from "./api";

const apiOrigin = process.env.API_INTERNAL_URL ?? "http://127.0.0.1:8088";

export function publicOrigin(): string {
  const value = process.env.PUBLIC_ORIGIN ?? process.env.WEB_INTERNAL_URL ?? "http://127.0.0.1:3088";
  const url = new URL(value);
  if (!["https:", "http:"].includes(url.protocol) || url.username || url.password || url.pathname !== "/" || url.search || url.hash) {
    throw new Error("PUBLIC_ORIGIN must be an HTTP(S) origin");
  }
  return url.origin;
}

async function publicData<T>(path: string): Promise<T | null> {
  const response = await fetch(`${apiOrigin}/api/v1${path}`, { cache: "no-store", signal: AbortSignal.timeout(5000) });
  if (response.status === 404) return null;
  if (!response.ok) throw new Error(`Public API returned ${response.status}`);
  return (await response.json()).data as T;
}

export function publicID(value: string): number | null {
  if (!/^[1-9]\d*$/.test(value)) return null;
  const id = Number(value);
  return Number.isSafeInteger(id) ? id : null;
}

export const getPublicProduct = cache((id: number) => publicData<Product>(`/catalog/${id}`));
export const getPublicArticle = cache((kind: "news" | "knowledge", id: number) => publicData<ContentArticle>(`/public-content/${kind}/${id}`));

export type PublicIndex = { total: number; items: { path: string; updated_at: string }[]; limit: number; offset: number };
export async function getPublicIndex(offset: number, limit = 1000): Promise<PublicIndex | null> {
  try { return await publicData<PublicIndex>(`/public-index?limit=${limit}&offset=${offset}`); }
  catch { return null; }
}

export function safeJSONLD(value: unknown): string {
  return JSON.stringify(value).replace(/</g, "\\u003c").replace(/\u2028/g, "\\u2028").replace(/\u2029/g, "\\u2029");
}

export const getSite = cache(async (): Promise<PublicSite | null> => {
  try {
    const response = await fetch(`${apiOrigin}/api/v1/site`, { cache: "no-store", signal: AbortSignal.timeout(3000) });
    if (!response.ok) return null;
    return (await response.json()).data as PublicSite;
  } catch { return null; }
});
