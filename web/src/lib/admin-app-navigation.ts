export function adminAppsReturnHref(value: string | string[] | undefined) {
  if (typeof value !== "string" || !value.startsWith("/")) return undefined;
  try {
    const url = new URL(value, "https://admin.local");
    if (url.origin !== "https://admin.local" || !["/admin/apps", "/admin/apps/store"].includes(url.pathname)) return undefined;
    return url.pathname + url.search;
  } catch {
    return undefined;
  }
}
