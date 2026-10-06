import "server-only";
import { getPublicIndex, publicOrigin } from "./site-server";

export const sitemapChunkSize = 10000;

export function xml(value: string): string {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&apos;");
}

export function xmlResponse(body: string): Response {
  return new Response(`<?xml version="1.0" encoding="UTF-8"?>${body}`, { headers: { "Content-Type": "application/xml; charset=utf-8", "Cache-Control": "no-store" } });
}

export async function publicURLCount(): Promise<number | null> {
  const index = await getPublicIndex(0, 1);
  return index && Number.isSafeInteger(index.total) && index.total >= 0 ? index.total : null;
}

export async function sitemapChunk(chunk: number): Promise<Response> {
  const offset = chunk * sitemapChunkSize;
  const first = await getPublicIndex(offset, 1000);
  if (!first) return new Response("Public index unavailable", { status: 503, headers: { "Cache-Control": "no-store" } });
  if (chunk > 0 && offset >= first.total) return new Response("Not found", { status: 404 });
  const origin = publicOrigin();
  const entries = chunk === 0 ? ["/", "/news", "/knowledgebase", "/downloads"].map(path => `<url><loc>${xml(origin + path)}</loc></url>`) : [];
  const pages = Math.min(10, Math.ceil((first.total - offset) / 1000));
  for (let page = 0; page < pages; page++) {
    const index = page === 0 ? first : await getPublicIndex(offset + page * 1000, 1000);
    if (!index || index.total !== first.total) return new Response("Public index changed; retry", { status: 503, headers: { "Cache-Control": "no-store" } });
    for (const item of index.items) {
      if (!/^\/(?:products|news|knowledgebase)\/[1-9]\d*$/.test(item.path)) continue;
      const updated = new Date(item.updated_at);
      entries.push(`<url><loc>${xml(origin + item.path)}</loc>${Number.isNaN(updated.valueOf()) ? "" : `<lastmod>${xml(updated.toISOString())}</lastmod>`}</url>`);
    }
  }
  return xmlResponse(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">${entries.join("")}</urlset>`);
}
