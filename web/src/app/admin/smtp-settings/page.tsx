import { SMTPSettingsView } from "@/components/smtp-settings";
import { requireUser } from "@/lib/session";
import { redirect } from "next/navigation";
export default async function Page() { const user = await requireUser(true); if (user.role !== "super_admin") redirect("/console/account"); return <SMTPSettingsView />; }
