import { redirect } from "next/navigation";
import { requireUser } from "@/lib/session";
import { adminEntry, can } from "@/lib/admin-navigation";
import { SiteThemeSettingsView } from "@/components/site-content-admin";
export default async function Page() { const user = await requireUser(true); if (!can(user, "settings.view")) redirect(adminEntry(user)); return <SiteThemeSettingsView />; }
