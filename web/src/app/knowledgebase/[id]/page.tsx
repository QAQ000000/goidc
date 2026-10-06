import { ContentDetail } from "@/components/public-content";
import type { Metadata } from "next";
import { getPublicArticle, publicID, publicOrigin, safeJSONLD } from "@/lib/site-server";
import { articleMetadata } from "@/lib/public-metadata";

type Props = { params: Promise<{ id: string }> };
export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const id = publicID((await params).id);
  return articleMetadata(id ? await getPublicArticle("knowledge", id) : null, `/knowledgebase/${id}`);
}
export default async function Page({ params }: Props) {
  const id = publicID((await params).id);
  const article = id ? await getPublicArticle("knowledge", id) : null;
  const jsonLD = article && safeJSONLD({ "@context": "https://schema.org", "@type": "Article", headline: article.title, description: article.excerpt, datePublished: article.published_at, dateModified: article.updated_at, mainEntityOfPage: `${publicOrigin()}/knowledgebase/${id}` });
  return <>{jsonLD && <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: jsonLD }} />}<ContentDetail type="knowledge" id={id ?? 0} initialArticle={article ?? undefined} /></>;
}
