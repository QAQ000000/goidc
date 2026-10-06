import { ContentList } from "@/components/public-content";
import type { Metadata } from "next";
import { publicOrigin } from "@/lib/site-server";
export const metadata: Metadata = { title: "知识库", alternates: { canonical: `${publicOrigin()}/knowledgebase` } };
export default function Page() { return <ContentList type="knowledge" />; }
