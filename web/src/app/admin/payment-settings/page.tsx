import { redirect } from "next/navigation";
import { ChannelInstancesView } from "@/components/channel-instances";
import { requireUser } from "@/lib/session";
import { adminEntry } from "@/lib/admin-navigation";

export default async function Page() {
  const user = await requireUser(true);
  if (user.role !== "super_admin") redirect(adminEntry(user));
  return <ChannelInstancesView title="支付接口" initialCategory="payment" />;
}
