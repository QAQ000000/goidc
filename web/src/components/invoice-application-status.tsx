import { Badge } from "./ui/badge";

export const applicationStatusLabels = { pending: "待审核", rejected: "已驳回", issued: "已开具" };

export function ApplicationStatus({ status }: { status: keyof typeof applicationStatusLabels }) {
  return <Badge variant="outline">{applicationStatusLabels[status]}</Badge>;
}
