import { AdminManualResourcesView } from "@/components/admin-manual-resources";
import { requireUser } from "@/lib/session";
import { adminEntry, can } from "@/lib/admin-navigation";
import { redirect } from "next/navigation";

export default async function Page() {
  const user = await requireUser(true);
  if (!can(user, "resources.view")) redirect(adminEntry(user));
  return <AdminManualResourcesView />;
}
