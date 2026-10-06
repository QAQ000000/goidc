import type { Metadata } from "next";
import type { ContentArticle } from "./api";
import { publicOrigin } from "./site-server";

export function articleMetadata(article: ContentArticle | null, path: string): Metadata {
  if (!article) return { robots: { index: false, follow: false } };
  const url = `${publicOrigin()}${path}`;
  const description = article.excerpt || article.body.slice(0, 160);
  return {
    title: article.title,
    description,
    alternates: { canonical: url },
    openGraph: { type: "article", url, title: article.title, description, publishedTime: article.published_at ?? undefined, modifiedTime: article.updated_at },
    twitter: { card: "summary", title: article.title, description },
  };
}
