import { NextRequest, NextResponse } from "next/server";
import type { SiteProfile, User } from "./lib/api";

export async function proxy(request: NextRequest) {
  const path = request.nextUrl.pathname;
  // These fixed public resources must stay installable/updatable while the
  // business API is unavailable or pages are in maintenance mode.
  if (path === "/notification-sw.js" || path === "/manifest.webmanifest") return NextResponse.next();
  const authRedirect = (target: string) => {
    const base = new URL(process.env.PUBLIC_ORIGIN ?? request.url);
    if (!["http:", "https:"].includes(base.protocol) || base.username || base.password || (process.env.PUBLIC_ORIGIN !== undefined && (base.pathname !== "/" || base.search || base.hash))) throw new Error("Invalid public redirect origin");
    return NextResponse.redirect(new URL(target, base.origin), { status: 307, headers: { "Cache-Control": "no-store" } });
  };
  // Next's configured rewrites match case-insensitively, including /API/.
  if (/^\/api(?:\/|$)/i.test(path)) {
    // NextRequest has no authoritative socket peer. Never vouch for an IP
    // supplied by a caller of this rewrite. Production Nginx sends /api/
    // directly to Go with its own forwarding chain; local Next traffic falls
    // back to the Go socket peer for rate limiting and audit attribution.
    const headers = new Headers(request.headers);
    headers.delete("x-forwarded-for");
    headers.delete("x-real-ip");
    headers.delete("forwarded");
    return NextResponse.next({ request: { headers } });
  }
  const adminRoute = path === "/admin" || path.startsWith("/admin/");
  if (adminRoute) {
    const origin = process.env.API_INTERNAL_URL ?? "http://127.0.0.1:8088";
    const cookie = request.cookies.get("zjmfgo_session");
    if (!cookie) return authRedirect("/admin-login");
    try {
      const response = await fetch(`${origin}/api/v1/me`, {
        headers: { Cookie: `zjmfgo_session=${cookie.value}` },
        cache: "no-store",
        signal: AbortSignal.timeout(3000),
      });
      if (response.ok) {
        const user = (await response.json()).data as User;
        if (user?.role === "admin" || user?.role === "super_admin") return NextResponse.next();
        return authRedirect("/");
      }
    } catch {}
    return authRedirect("/admin-login");
  }
  if (["/admin-login", "/maintenance", "/forgot-password", "/reset-password"].includes(path)) return NextResponse.next();
  const origin = process.env.API_INTERNAL_URL ?? "http://127.0.0.1:8088";
  const customerRoute = path === "/console" || path.startsWith("/console/") || path === "/cart" || path.startsWith("/cart/");
  const customerAuthRoute = path === "/login" || path === "/register";
  const cookie = request.cookies.get("zjmfgo_session");
  let user: User | null = null;
  let accountStatus = 0;
  if (cookie && (customerRoute || customerAuthRoute)) {
    try {
      const response = await fetch(`${origin}/api/v1/me`, {
        headers: { Cookie: `zjmfgo_session=${cookie.value}` },
        cache: "no-store",
        signal: AbortSignal.timeout(3000),
      });
      accountStatus = response.status;
      if (response.ok) user = (await response.json()).data as User;
    } catch {}
    // Staff can preview public pages, but customer workspaces and login forms
    // must never treat an administrator session as a customer session.
    if (user?.role === "admin" || user?.role === "super_admin") return authRedirect(path === "/console/account" ? "/admin/account" : "/admin");
  }
  let site: SiteProfile | null = null;
  try {
    const response = await fetch(`${origin}/api/v1/site`, { cache: "no-store", signal: AbortSignal.timeout(3000) });
    if (response.ok) site = (await response.json()).data as SiteProfile;
  } catch {}
  if (site && !site.maintenance_enabled) {
    if (!customerRoute) return NextResponse.next();
    // A nested layout redirect can become a streamed 200 + client meta redirect.
    // Resolve the console session before rendering to preserve the HTTP redirect.
    if (!cookie) return authRedirect("/login");
    if (accountStatus === 401) return authRedirect("/login");
    if (user?.role === "customer" && Number.isSafeInteger(user.id) && user.id > 0) return NextResponse.next();
    return new NextResponse("账户服务暂时不可用，请稍后再试。", { status: 503, headers: { "Cache-Control": "no-store", "Retry-After": "60", "Content-Type": "text/plain; charset=utf-8" } });
  }
  if (cookie && !customerRoute && !customerAuthRoute) {
    try {
      const response = await fetch(`${origin}/api/v1/me`, { headers: { Cookie: `zjmfgo_session=${cookie.value}` }, cache: "no-store", signal: AbortSignal.timeout(3000) });
      if (response.ok) {
        const user = (await response.json()).data as User;
        if (user.role === "admin" || user.role === "super_admin") return NextResponse.next();
      }
    } catch {}
  }
  if (site?.maintenance_url) return NextResponse.redirect(site.maintenance_url, { status: 307, headers: { "Cache-Control": "no-store" } });
  const headers = { "Cache-Control": "no-store", "Retry-After": "60", "Content-Type": "text/html; charset=utf-8" };
  try {
    // App page rendering resets a rewrite's status to 200. Return the rendered maintenance document as a 503 response.
    const webOrigin = process.env.WEB_INTERNAL_URL ?? "http://127.0.0.1:3088";
    const response = await fetch(`${webOrigin}/maintenance`, { cache: "no-store", signal: AbortSignal.timeout(8000) });
    if (response.ok) return new NextResponse(response.body, { status: 503, headers });
  } catch {}
  return new NextResponse("站点暂时不可用，请稍后再试。", { status: 503, headers: { ...headers, "Content-Type": "text/plain; charset=utf-8" } });
}

export const config = { matcher: ["/api/:path*", "/((?!api/|_next/|assets/|favicon.ico|notification-sw\\.js$|manifest\\.webmanifest$).*)"] };
