import { AdminTrafficOrderDetailView } from "@/components/admin-traffic";
import { requireUser } from "@/lib/session";
import { can, adminEntry } from "@/lib/admin-navigation";
import { redirect } from "next/navigation";
export default async function Page({ params }: { params: Promise<{ id: string }> }) { const user = await requireUser(true); if (!can(user, "orders.view")) redirect(adminEntry(user)); const { id } = await params; return <AdminTrafficOrderDetailView id={Number(id)} />; }
