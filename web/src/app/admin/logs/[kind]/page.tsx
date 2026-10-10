import { AdminLogPage } from "@/components/admin-log-page";
export default async function Page({ params }: { params: Promise<{ kind: string }> }) { return <AdminLogPage kind={(await params).kind} />; }
