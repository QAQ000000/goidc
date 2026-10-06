import { ProductEditorView } from "@/components/product-editor";
import { requireUser } from "@/lib/session";
import { can, adminEntry } from "@/lib/admin-navigation";
import { redirect, notFound } from "next/navigation";
export default async function Page({params}:{params:Promise<{id:string}>}) {
 const user=await requireUser(true);if(!can(user,"products.view"))redirect(adminEntry(user));
 const {id}=await params;const value=Number(id);if(!Number.isSafeInteger(value)||value<1)notFound();
 return <ProductEditorView id={value}/>;
}
