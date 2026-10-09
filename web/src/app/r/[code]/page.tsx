"use client";

import { useEffect, useState } from "react";
import { useParams } from "next/navigation";
import { Loading } from "@/components/common";
import { api } from "@/lib/api";

export default function AffiliateRedirectPage() {
  const params = useParams<{ code: string }>();
  const [message, setMessage] = useState("正在打开推广页面…");
  useEffect(() => {
    let active = true;
    void api<{ landing_path: string; valid: boolean }>(`/affiliate/click/${encodeURIComponent(params.code)}`)
      .then(result => {
        if (!active) return;
        if (!result.valid) { setMessage("推广链接已失效"); return; }
        const target = new URL(result.landing_path, window.location.origin);
        if (target.origin !== window.location.origin || !target.pathname.startsWith("/")) { setMessage("推广落地页无效"); return; }
        window.location.replace(`${target.pathname}${target.search}${target.hash}`);
      })
      .catch(() => { if (active) setMessage("推广链接暂时无法打开"); });
    return () => { active = false; };
  }, [params.code]);
  return <main className="flex min-h-[60vh] items-center justify-center p-6"><div className="text-center"><Loading /><p className="text-sm text-muted-foreground">{message}</p></div></main>;
}
