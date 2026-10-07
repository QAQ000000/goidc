import { redirect } from "next/navigation";
import { PaymentSettingsView } from "@/components/payment-settings";
import { requireUser } from "@/lib/session";

export default async function Page() {
  const user = await requireUser(true);
  if (user.role !== "super_admin") redirect("/console/account");
  return <PaymentSettingsView />;
}
