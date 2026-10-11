import { SiteSettingsView } from "@/components/site-settings";
import { requireUser } from "@/lib/session";
import { redirect } from "next/navigation";
export default async function Page() { const user = await requireUser(true); if (user.role !== "super_admin" && !user.permissions?.includes("settings.view")) redirect("/console/account"); return <SiteSettingsView />; }
