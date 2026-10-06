import { notFound,redirect } from "next/navigation";
import { InvoiceApplicationDetailView } from "@/components/invoice-applications";
import { requireUser } from "@/lib/session";
import { adminEntry,can } from "@/lib/admin-navigation";
export default async function Page({params}:{params:Promise<{id:string}>}){const user=await requireUser(true);if(!can(user,"invoice_applications.view"))redirect(adminEntry(user));const {id}=await params;const value=Number(id);if(!Number.isSafeInteger(value)||value<1)notFound();return <InvoiceApplicationDetailView id={value} admin/>;}
