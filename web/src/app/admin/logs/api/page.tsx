import { redirect } from "next/navigation";
import { AdminNotificationLogs } from "@/components/admin-notification-logs";
import { adminEntry, can } from "@/lib/admin-navigation";
import { requireUser } from "@/lib/session";

export default async function Page() {
  const user = await requireUser(true);
  if (!can(user, "audit.view")) redirect(adminEntry(user));
  return <AdminNotificationLogs kind="api" />;
}
