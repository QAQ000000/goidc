import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { CatalogView } from "@/components/catalog-view";
import { getPublicProduct, publicID, publicOrigin, safeJSONLD } from "@/lib/site-server";

type Props = { params: Promise<{ id: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const id = publicID((await params).id);
  const product = id && await getPublicProduct(id);
  if (!product) return { robots: { index: false, follow: false } };
  const url = `${publicOrigin()}/products/${id}`;
  return {
    title: product.name,
    description: product.description || product.name,
    alternates: { canonical: url },
    openGraph: { type: "website", url, title: product.name, description: product.description || product.name },
    twitter: { card: "summary", title: product.name, description: product.description || product.name },
  };
}

export default async function Page({ params }: Props) {
  const id = publicID((await params).id);
  if (!id) notFound();
  const product = await getPublicProduct(id);
  if (!product) notFound();
  const jsonLD = safeJSONLD({
    "@context": "https://schema.org",
    "@type": "Product",
    name: product.name,
    description: product.description,
    url: `${publicOrigin()}/products/${id}`,
  });
  return <><script type="application/ld+json" dangerouslySetInnerHTML={{ __html: jsonLD }} /><CatalogView initialProduct={product} /></>;
}
