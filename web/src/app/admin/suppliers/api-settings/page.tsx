import { SupplierAPISettingsView } from "@/components/supplier-api-settings";
import { requireUser } from "@/lib/session";
import { adminEntry, can } from "@/lib/admin-navigation";
import { redirect } from "next/navigation";

export default async function Page() {
  const user = await requireUser(true);
  if (!can(user, "products.view")) redirect(adminEntry(user));
  return <SupplierAPISettingsView />;
}
