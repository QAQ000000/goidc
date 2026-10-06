import { OrderDetailView } from "@/components/data-pages";

export default async function OrderPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return <OrderDetailView id={Number(id)} />;
}
