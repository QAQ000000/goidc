import type { Metadata } from "next";
export const metadata: Metadata = { robots: { index: false, follow: false } };
import { redirect } from "next/navigation";
import { getSite } from "@/lib/site-server";
import { MaintenanceScreen } from "@/components/site-availability";
export default async function Page() {
  const site = await getSite();
  if (site && !site.maintenance_enabled) redirect("/");
  return <MaintenanceScreen />;
}
