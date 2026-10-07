import { ChannelInstancesView } from "@/components/channel-instances";
import { requireUser } from "@/lib/session";
import { redirect } from "next/navigation";
export default async function Page() { const user = await requireUser(true); if (user.role !== "super_admin") redirect("/console/account"); return <ChannelInstancesView title="邮件接口" initialCategory="mail" />; }
