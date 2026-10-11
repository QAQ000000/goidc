import { AdminTrafficOrdersView } from "@/components/admin-traffic";
import { requireUser } from "@/lib/session";
import { can, adminEntry } from "@/lib/admin-navigation";
import { redirect } from "next/navigation";
export default async function Page() { const user = await requireUser(true); if (!can(user, "orders.view")) redirect(adminEntry(user)); return <AdminTrafficOrdersView />; }
