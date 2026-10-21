import type { Metadata } from "next";
export const metadata: Metadata = { robots: { index: false, follow: false } };
import { requireUser } from "@/lib/session";
import { AdminAccessProvider } from "@/components/workspace";
import { cookies } from "next/headers";
import { SidebarProvider } from "@/components/ui/sidebar";
export default async function Layout({children}:{children:React.ReactNode}) {
  const user=await requireUser(true);
  const sidebarOpen=(await cookies()).get("goidc_admin_sidebar_state")?.value!=="false";
  return <AdminAccessProvider initialUser={user}><div className="admin-theme"><SidebarProvider defaultOpen={sidebarOpen} className="dashboard-shell">{children}</SidebarProvider></div></AdminAccessProvider>;
}
