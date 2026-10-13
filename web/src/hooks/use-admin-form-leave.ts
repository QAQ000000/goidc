"use client";

import { useEffect } from "react";

export function useAdminFormLeave(dirty: boolean) {
  useEffect(() => {
    if (!dirty) return;
    const original = window.location.href;
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    const leave = (event: MouseEvent) => {
      const link = (event.target as Element)?.closest("a[href]");
      if (!link || event.defaultPrevented) return;
      if (!window.confirm("有未保存的内容或待确认请求，确定离开吗？")) {
        event.preventDefault(); event.stopPropagation();
      }
    };
    const back = (event: PopStateEvent) => {
      if (!window.confirm("有未保存的内容或待确认请求，确定离开吗？")) {
        event.stopImmediatePropagation();
        window.history.pushState(null, "", original);
      }
    };
    window.addEventListener("beforeunload", warn);
    document.addEventListener("click", leave, true);
    window.addEventListener("popstate", back, true);
    return () => { window.removeEventListener("beforeunload", warn); document.removeEventListener("click", leave, true); window.removeEventListener("popstate", back, true); };
  }, [dirty]);
}
