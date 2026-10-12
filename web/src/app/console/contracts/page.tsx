import { ContractsView } from "@/components/contracts";
import { requireUser } from "@/lib/session";
export default async function Page() { const user = await requireUser(); return <ContractsView identity={user.id} />; }
