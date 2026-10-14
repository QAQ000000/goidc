"use client";
import { useEffect, useRef } from "react";
import { usePathname, useRouter } from "next/navigation";

// 需要登录的页面；在这些页面上收到 401 说明会话已失效，应跳转登录页。
// 公开页面（例如匿名访问时对 /me 的探测）不会触发跳转。
function privateRoute(path: string) {
  return path === "/admin" || path.startsWith("/admin/") || path.startsWith("/console/") || path === "/cart" || path.startsWith("/cart/");
}

// 监听 api 层派发的 zjmfgo:unauthorized 事件，统一跳转到对应登录页：
// 管理端 → /admin-login，客户中心 → /login。
// 登录/注册等凭据端点自身的 401 已在 api.ts 中排除，不会被当成会话过期，也不会形成循环。
export function useUnauthorizedRedirect() {
  const path = usePathname();
  const router = useRouter();
  const redirecting = useRef(false);
  useEffect(() => {
    redirecting.current = false;
    if (!privateRoute(path)) return;
    const handler = () => {
      if (redirecting.current) return;
      redirecting.current = true;
      router.replace(loginHref(path));
    };
    window.addEventListener("zjmfgo:unauthorized", handler);
    return () => window.removeEventListener("zjmfgo:unauthorized", handler);
  }, [path, router]);
}

export function loginHref(path: string) {
  return path === "/admin" || path.startsWith("/admin/") ? "/admin-login" : "/login";
}
