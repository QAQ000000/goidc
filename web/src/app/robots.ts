import type { MetadataRoute } from "next";
import { publicOrigin } from "@/lib/site-server";

export const dynamic = "force-dynamic";
export default function robots(): MetadataRoute.Robots {
  return {
    rules: { userAgent: "*", allow: "/", disallow: ["/admin/", "/console/", "/cart", "/checkout", "/login", "/api/"] },
    sitemap: `${publicOrigin()}/sitemap.xml`,
  };
}
