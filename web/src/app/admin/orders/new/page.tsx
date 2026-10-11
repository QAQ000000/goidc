import { redirect } from "next/navigation";
import { requireUser } from "@/lib/session";
import { adminEntry, can } from "@/lib/admin-navigation";
import { AdminOrderCreateView } from "@/components/admin-order-create";

export default async function Page() {
  const user = await requireUser(true);
  if (!can(user, "orders.manage")) redirect(adminEntry(user));
  return <AdminOrderCreateView />;
}
