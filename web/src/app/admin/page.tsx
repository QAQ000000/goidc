import { DashboardView } from "@/components/data-pages";
import { requireUser } from "@/lib/session";
import { adminEntry, can } from "@/lib/admin-navigation";
import { redirect } from "next/navigation";
export default async function Page() {
  const user = await requireUser(true);
  if (!can(user, "dashboard.view")) redirect(adminEntry(user));
  return <DashboardView />;
}
