import type { MetadataRoute } from "next";
import { publicOrigin } from "@/lib/site-server";

export const dynamic = "force-dynamic";
export default function robots(): MetadataRoute.Robots {
  return {
    rules: { userAgent: "*", allow: "/", disallow: ["/admin/", "/console/", "/cart", "/checkout", "/login", "/admin-login", "/register", "/forgot-password", "/reset-password", "/maintenance", "/api/"] },
    sitemap: `${publicOrigin()}/sitemap.xml`,
  };
}
