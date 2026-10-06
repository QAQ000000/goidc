import { notFound } from "next/navigation";
import { ServiceDetailView } from "@/components/data-pages";

export default async function Page({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const value = Number(id);
  if (!Number.isSafeInteger(value) || value < 1) notFound();
  return <ServiceDetailView id={value} />;
}
