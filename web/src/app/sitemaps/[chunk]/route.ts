import { sitemapChunk } from "@/lib/sitemap";

export const dynamic = "force-dynamic";
export async function GET(_request: Request, { params }: { params: Promise<{ chunk: string }> }) {
  const value = (await params).chunk;
  if (!/^(0|[1-9]\d*)\.xml$/.test(value)) return new Response("Not found", { status: 404 });
  const chunk = Number(value.slice(0, -4));
  if (!Number.isSafeInteger(chunk) || !Number.isSafeInteger(chunk * 10000)) return new Response("Not found", { status: 404 });
  return sitemapChunk(chunk);
}
