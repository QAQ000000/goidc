import { redirect } from "next/navigation";
import { AdminAppsView } from "@/components/admin-apps";
import { adminEntry } from "@/lib/admin-navigation";
import { requireUser } from "@/lib/session";

export default async function Page() {
  const user = await requireUser(true);
  if (user.role !== "super_admin") redirect(adminEntry(user));
  return <AdminAppsView view="catalogue" />;
}
