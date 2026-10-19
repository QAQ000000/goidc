import type { Metadata } from "next";
export const metadata: Metadata = { robots: { index: false, follow: false } };
import { CartView } from "@/components/cart-view";
import { requireUser } from "@/lib/session";
export default async function Page() {
  await requireUser();
  return <CartView />;
}
