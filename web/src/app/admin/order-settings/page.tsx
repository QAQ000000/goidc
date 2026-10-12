import {redirect} from "next/navigation";
import {OrderSettingsView} from "@/components/order-settings";
import {requireUser} from "@/lib/session";
import {can,adminEntry} from "@/lib/admin-navigation";
export default async function Page(){const user=await requireUser(true);if(!can(user,"settings.view"))redirect(adminEntry(user));return <OrderSettingsView/>}
