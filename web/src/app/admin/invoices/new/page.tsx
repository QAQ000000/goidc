import { ManualInvoiceEditorView } from "@/components/manual-invoices";
import { requireUser } from "@/lib/session";
import { can, adminEntry } from "@/lib/admin-navigation";
import { redirect } from "next/navigation";

export default async function Page() {
  const user = await requireUser(true);
  if (!can(user, "invoices.manage")) redirect(adminEntry(user));
  return <ManualInvoiceEditorView />;
}
