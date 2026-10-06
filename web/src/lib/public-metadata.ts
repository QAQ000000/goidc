import type { Metadata } from "next";
import type { ContentArticle } from "./api";
import { publicOrigin } from "./site-server";

export function articleMetadata(article: ContentArticle | null, path: string): Metadata {
  if (!article) return { robots: { index: false, follow: false } };
  const url = `${publicOrigin()}${path}`;
  const description = article.seo_description || article.excerpt || article.body.slice(0, 160);
  const title = article.seo_title || article.title;
  const canonical = article.seo_canonical ? `${publicOrigin()}${article.seo_canonical}` : url;
  return {
    title,
    description,
    alternates: { canonical },
    openGraph: { type: "article", url: canonical, title, description, publishedTime: article.published_at ?? undefined, modifiedTime: article.updated_at },
    twitter: { card: "summary", title, description },
  };
}
