import { CatalogView } from "@/components/catalog-view";
import type { Metadata } from "next";
import { publicOrigin } from "@/lib/site-server";
export const metadata: Metadata = { alternates: { canonical: publicOrigin() } };
export default function Page() {
  return <CatalogView />;
}
