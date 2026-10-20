"use client";

import { useRef, useState } from "react";
import { LogIn } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "./ui/button";
import { useAdminPermission } from "./workspace";

export type CustomerImpersonationTarget = {
  id: number;
  username: string;
  display_name: string;
  status: string;
};

export function CustomerImpersonationButton({ customer }: { customer: CustomerImpersonationTarget }) {
  const manage = useAdminPermission("customers.manage");
  const submitting = useRef(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");
  const target = `${customer.display_name || customer.username}（#${customer.id}）`;
  const active = customer.status === "active";

  async function start() {
    if (!manage || !active || submitting.current) return;
    if (!window.confirm(
      `将以客户 ${target} 的身份在新标签页打开客户中心，当前后台会保留。\n\n本浏览器的所有客户页面共用此客户身份，原客户登录会被替换；退出代登录不会恢复原客户会话。继续吗？`,
    )) return;

    submitting.current = true;
    setBusy(true);
    setError("");
    setSuccess("");
    let popup: Window | null = null;
    try {
      try {
        popup = window.open("about:blank", "_blank");
        if (!popup) throw new Error("浏览器阻止了新标签页，请允许弹出窗口后重试。");
        popup.opener = null;
        popup.document.title = "正在进入客户中心";
        popup.document.body.textContent = `正在以客户 ${target} 的身份进入客户中心，请稍候。`;
        if (popup.closed) throw new Error("新标签页已关闭，请重新打开后再试。");
      } catch (popupError) {
        setError(popupError instanceof Error ? popupError.message : "无法打开客户中心，请允许弹出窗口后重试。");
        try { popup?.close(); } catch { /* The browser may already have closed the tab. */ }
        return;
      }

      try {
        await api(`/admin/customers/${customer.id}/impersonate`, { method: "POST", body: "{}" });
      } catch (requestError) {
        try { popup.close(); } catch { /* The browser may already have closed the tab. */ }
        setError(requestError instanceof Error ? requestError.message : "代登录失败，请稍后重试。");
        return;
      }

      // The customer cookie is already set. A closed tab or navigation failure
      // must not be reported as a failed login or retried automatically.
      let opened = false;
      try {
        if (!popup.closed) {
          popup.location.replace("/console/services");
          opened = true;
        }
      } catch { /* Keep the manual link available after successful login. */ }
      setSuccess(opened
        ? `已以客户 ${target} 的身份登录，客户中心已在新标签页打开。`
        : `已以客户 ${target} 的身份登录，可手动打开客户中心。`);
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  if (!manage) return null;

  return (
    <div className="w-full">
      <Button
        type="button"
        disabled={busy || !active}
        onClick={() => void start()}
        className="w-full bg-[#409eff] hover:bg-[#337ecc] text-white text-xs h-9 rounded font-normal"
      >
        <LogIn size={14} className="mr-1.5" />
        {busy ? "登录中..." : "以该客户登录"}
      </Button>
      {!active && <p className="text-xs text-muted-foreground mt-1">仅正常状态的客户可代登录。</p>}
      {error && <p role="alert" className="text-xs text-destructive mt-1">{error}</p>}
      {success && (
        <div role="status" className="text-xs text-muted-foreground mt-1 space-y-1">
          <p>{success}</p>
          <a href="/console/services" target="_blank" rel="noopener noreferrer" className="text-[#409eff] underline underline-offset-4">打开客户中心</a>
        </div>
      )}
    </div>
  );
}
