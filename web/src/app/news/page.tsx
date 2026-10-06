import { ContentList } from "@/components/public-content";
import type { Metadata } from "next";
import { publicOrigin } from "@/lib/site-server";
export const metadata: Metadata = { title: "新闻中心", alternates: { canonical: `${publicOrigin()}/news` } };
export default function Page() { return <ContentList type="news" />; }
