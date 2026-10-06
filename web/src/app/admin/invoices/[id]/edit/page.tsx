import { notFound } from "next/navigation";
import { ManualInvoiceEditorView } from "@/components/manual-invoices";
export default async function Page({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  const value = Number(id);
  if (!Number.isSafeInteger(value) || value < 1) notFound();
  return <ManualInvoiceEditorView id={value} />;
}
