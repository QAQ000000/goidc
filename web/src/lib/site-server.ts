import "server-only";
import { cache } from "react";
import type { SiteProfile } from "./api";

export const getSite = cache(async (): Promise<SiteProfile | null> => {
  try {
    const response = await fetch(`${process.env.API_INTERNAL_URL ?? "http://127.0.0.1:8088"}/api/v1/site`, { cache: "no-store", signal: AbortSignal.timeout(3000) });
    if (!response.ok) return null;
    return (await response.json()).data as SiteProfile;
  } catch { return null; }
});
