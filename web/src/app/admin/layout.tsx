import type { Metadata } from "next";
export const metadata: Metadata = { robots: { index: false, follow: false } };
import { requireUser } from "@/lib/session";
export default async function Layout({children}:{children:React.ReactNode}){await requireUser(true);return <div className="admin-theme">{children}</div>;}
