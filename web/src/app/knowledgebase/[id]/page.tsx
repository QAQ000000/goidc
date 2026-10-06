import { ContentDetail } from "@/components/public-content";
export default async function Page({ params }: { params: Promise<{ id: string }> }) { const { id } = await params; return <ContentDetail type="knowledge" id={Number(id)} />; }
