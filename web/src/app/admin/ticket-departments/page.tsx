import { TicketDepartmentsView } from "@/components/ticket-departments";
import { requireUser } from "@/lib/session";
import { adminEntry } from "@/lib/admin-navigation";
import { redirect } from "next/navigation";
export default async function Page() {
  const user = await requireUser(true);
  if (user.role !== "super_admin") redirect(adminEntry(user));
  return <TicketDepartmentsView />;
}
