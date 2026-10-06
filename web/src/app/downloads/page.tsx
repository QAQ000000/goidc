import type { Metadata } from "next";
import { publicOrigin } from "@/lib/site-server";
export const metadata: Metadata = { alternates: { canonical: `${publicOrigin()}/downloads` } };
import { DownloadsView } from "@/components/downloads";
export default function Page() { return <DownloadsView/>; }
