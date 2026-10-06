import { publicOrigin } from "@/lib/site-server";
import { publicURLCount, sitemapChunkSize, xml, xmlResponse } from "@/lib/sitemap";

export const dynamic = "force-dynamic";
export async function GET() {
  const total = await publicURLCount();
  if (total === null) return new Response("Public index unavailable", { status: 503, headers: { "Cache-Control": "no-store" } });
  const origin = publicOrigin();
  const chunks = Math.max(1, Math.ceil(total / sitemapChunkSize));
  const entries = Array.from({ length: chunks }, (_, id) => `<sitemap><loc>${xml(`${origin}/sitemaps/${id}.xml`)}</loc></sitemap>`);
  return xmlResponse(`<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">${entries.join("")}</sitemapindex>`);
}
