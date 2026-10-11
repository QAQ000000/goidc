import type { Metadata } from "next";
export const metadata: Metadata = { robots: { index: false, follow: false } };
import { requireUser } from "@/lib/session";
import { AdminAccessProvider } from "@/components/workspace";
export default async function Layout({children}:{children:React.ReactNode}){const user=await requireUser(true);return <AdminAccessProvider initialUser={user}><div className="admin-theme">{children}</div></AdminAccessProvider>;}
