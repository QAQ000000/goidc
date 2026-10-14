import { requireUser } from "@/lib/session";
import { adminEntry } from "@/lib/admin-navigation";
import { redirect } from "next/navigation";
import { AdminBlacklist } from "@/components/admin-blacklist";

export default async function Page() {
  const user = await requireUser(true);
  if (user.role !== "super_admin" && !user.permissions?.includes("security.view")) redirect(adminEntry(user));
  return <AdminBlacklist />;
}
