import { AdminAccountView } from "@/components/admin-account";
import { requireUser } from "@/lib/session";

export default async function Page() {
  await requireUser(true);
  return <AdminAccountView />;
}
