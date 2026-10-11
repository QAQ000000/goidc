import { notFound, redirect } from "next/navigation";
import { AdminLogPage } from "@/components/admin-log-page";

export default async function Page({ params }: { params: Promise<{ kind: string }> }) {
  const { kind } = await params;
  // Dedicated log pages own their data, permissions and maintenance controls.
  if (kind === "login") redirect("/admin/logs/admin-login");
  if (kind === "cron") redirect("/admin/logs/jobs");
  if (kind === "message") redirect("/admin/logs/inbox");
  if (kind !== "system") notFound();
  return <AdminLogPage />;
}
