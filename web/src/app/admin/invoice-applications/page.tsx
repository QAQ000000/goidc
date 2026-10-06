import { redirect } from "next/navigation";
import { InvoiceApplicationListView } from "@/components/invoice-applications";
import { requireUser } from "@/lib/session";
import { adminEntry,can } from "@/lib/admin-navigation";
export default async function Page(){const user=await requireUser(true);if(!can(user,"invoice_applications.view"))redirect(adminEntry(user));return <InvoiceApplicationListView admin/>;}
