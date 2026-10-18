import { redirect } from "next/navigation";
import { requireUser } from "@/lib/session";
import { adminEntry } from "@/lib/admin-navigation";
import { ChannelInstancesView, type ChannelCategory } from "@/components/channel-instances";
import { adminAppsReturnHref } from "@/lib/admin-app-navigation";

const categories: readonly ChannelCategory[] = ["payment", "oauth", "sms", "mail", "bot", "certification", "provision"];

export default async function Page({ searchParams }: { searchParams: Promise<{ category?: string | string[]; return_to?: string | string[] }> }) {
  const user = await requireUser(true);
  if (user.role !== "super_admin") redirect(adminEntry(user));
  const params = await searchParams;
  const requestedCategory = params.category;
  const category = typeof requestedCategory === "string" && categories.includes(requestedCategory as ChannelCategory) ? requestedCategory as ChannelCategory : "payment";
  return <ChannelInstancesView key={category} title="接口设置" initialCategory={category} returnHref={adminAppsReturnHref(params.return_to)} />;
}
