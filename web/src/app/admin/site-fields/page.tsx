import { redirect } from "next/navigation";
import { requireUser } from "@/lib/session";
import { adminEntry, can } from "@/lib/admin-navigation";
import { SiteContentCrudView } from "@/components/site-content-admin";
export default async function Page() { const user = await requireUser(true); if (!can(user, "content.view")) redirect(adminEntry(user)); return <SiteContentCrudView kind="fields" />; }
