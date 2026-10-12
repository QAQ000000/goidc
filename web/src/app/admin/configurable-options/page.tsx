import { redirect } from "next/navigation";
import { requireUser } from "@/lib/session";
import { can, adminEntry } from "@/lib/admin-navigation";
import { GlobalConfigurationView } from "@/components/global-configuration";
export default async function Page(){const user=await requireUser(true);if(!can(user,"products.view"))redirect(adminEntry(user));return <GlobalConfigurationView/>;}
