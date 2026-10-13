import { NotificationTemplateCreateView } from "@/components/notifications";
import { requireUser } from "@/lib/session";
import { can, adminEntry } from "@/lib/admin-navigation";
import { redirect } from "next/navigation";
export default async function Page(){const user=await requireUser(true);if(!can(user,"notifications.view"))redirect(adminEntry(user));return <NotificationTemplateCreateView/>;}
