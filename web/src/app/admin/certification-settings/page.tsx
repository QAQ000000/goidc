import { redirect } from "next/navigation";
import { CertificationSettingsView } from "@/components/certification-settings";
import { requireUser } from "@/lib/session";
import { adminEntry, can } from "@/lib/admin-navigation";

export default async function Page() {
  const user = await requireUser(true);
  if (!can(user, "certifications.view")) redirect(adminEntry(user));
  return <CertificationSettingsView />;
}
