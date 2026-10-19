import type { Metadata } from "next";
import "./globals.css";
import { getSite, publicOrigin } from "@/lib/site-server";
import { SiteProvider } from "@/components/site-provider";
import { SiteAvailability } from "@/components/site-availability";
export const dynamic = "force-dynamic";
export async function generateMetadata(): Promise<Metadata> {
  const site = await getSite();
  return { title: site?.site_name ?? "智简魔方", description: site?.description ?? "云服务、订单与财务管理", metadataBase: new URL(publicOrigin()), icons: { icon: site?.logo_url || "/assets/brand-logo.png", apple: "/assets/notification-icon-192.png" } };
}
export default async function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  const site = await getSite();
  return (
    <html lang="zh-CN">
      <body><SiteProvider initial={site}><SiteAvailability>{children}</SiteAvailability></SiteProvider></body>
    </html>
  );
}
