import { notFound, redirect } from "next/navigation";
import { AdminStaffEditView } from "@/components/admin-staff-edit";
import { requireUser } from "@/lib/session";
import { adminEntry } from "@/lib/admin-navigation";

export default async function Page({ params }: { params: Promise<{ id: string }> }) {
  const user = await requireUser(true);
  if (user.role !== "super_admin") redirect(adminEntry(user));
  const { id } = await params;
  if (!/^[1-9]\d*$/.test(id) || !Number.isSafeInteger(Number(id))) notFound();
  return <AdminStaffEditView id={Number(id)} />;
}
