import { SuppliersView } from "@/components/suppliers-admin";
import { requireUser } from "@/lib/session";
import { can, adminEntry } from "@/lib/admin-navigation";
import { redirect } from "next/navigation";
import { adminAppsReturnHref } from "@/lib/admin-app-navigation";

export default async function Page({ searchParams }: { searchParams: Promise<{ return_to?: string | string[] }> }) {
  const user = await requireUser(true);
  if (!can(user, "products.view")) redirect(adminEntry(user));
  return <SuppliersView returnHref={adminAppsReturnHref((await searchParams).return_to)} />;
}
