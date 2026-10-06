import type { Metadata } from "next";
export const metadata: Metadata = { robots: { index: false, follow: false } };
import { CartView } from "@/components/cart-view";
export default function Page() {
  return <CartView />;
}
