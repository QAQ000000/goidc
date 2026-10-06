import { notFound,redirect } from "next/navigation";
import { requireUser } from "@/lib/session";
import { can,adminEntry } from "@/lib/admin-navigation";
import { AdminCustomerFilesView } from "@/components/customer-files";
export default async function Page({params}:{params:Promise<{id:string}>}){const user=await requireUser(true);if(!can(user,"customer_files.view"))redirect(adminEntry(user));const {id}=await params;const value=Number(id);if(!Number.isSafeInteger(value)||value<1)notFound();return <AdminCustomerFilesView customerId={value}/>;}
