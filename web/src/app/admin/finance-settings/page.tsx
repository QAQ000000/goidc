import { FinanceSettingsView } from "@/components/financial-settings";
import { requireUser } from "@/lib/session";
import { can, adminEntry } from "@/lib/admin-navigation";
import { redirect } from "next/navigation";
export default async function Page() { const user = await requireUser(true); if (!can(user, "settings.view")) redirect(adminEntry(user)); return <FinanceSettingsView />; }
