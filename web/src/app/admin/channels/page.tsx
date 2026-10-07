import { redirect } from "next/navigation";
import { requireUser } from "@/lib/session";
import { ChannelInstancesView } from "@/components/channel-instances";

export default async function Page() {
  const user = await requireUser(true);
  if (user.role !== "super_admin") redirect("/console/account");
  return <ChannelInstancesView />;
}
