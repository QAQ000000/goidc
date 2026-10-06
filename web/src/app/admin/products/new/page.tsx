import { ProductEditorView } from "@/components/product-editor";
import { requireUser } from "@/lib/session";
import { can, adminEntry } from "@/lib/admin-navigation";
import { redirect } from "next/navigation";
export default async function Page(){const user=await requireUser(true);if(!can(user,"products.manage"))redirect(adminEntry(user));return <ProductEditorView/>;}
