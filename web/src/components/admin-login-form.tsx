"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { api, type User } from "@/lib/api";
import { useSite } from "./site-provider";
import { CaptchaField, useCaptcha } from "./captcha-field";

export function AdminLoginForm() {
  const router = useRouter();
  const { site, refresh: refreshSite } = useSite();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [rememberMe, setRememberMe] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  // MFA Challenge State
  const [mfaChallenge, setMfaChallenge] = useState("");
  const [mfaCode, setMfaCode] = useState("");

  const captcha = useCaptcha({ action: "login", active: !!site?.auth_captcha_enabled && !mfaChallenge, refreshSite });
  const refreshCaptcha = captcha.refresh;

  // Initialize rememberMe from localStorage
  useEffect(() => {
    const timer = window.setTimeout(() => {
      try {
        const savedUser = localStorage.getItem("zjmf_admin_remember_user");
        if (savedUser) { setUsername(savedUser); setRememberMe(true); }
      } catch { /* Storage may be unavailable in private browsing. */ }
    }, 0);
    return () => window.clearTimeout(timer);
  }, []);

  async function submit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (busy || !captcha.canSubmit()) return;
    setBusy(true);
    setError("");

    try {
      if (rememberMe) {
        try {
          localStorage.setItem("zjmf_admin_remember_user", username);
        } catch {
          // ignore
        }
      } else {
        try {
          localStorage.removeItem("zjmf_admin_remember_user");
        } catch {
          // ignore
        }
      }

      const user = mfaChallenge
        ? await api<User>("/auth/login/mfa", {
            method: "POST",
            body: JSON.stringify({ challenge: mfaChallenge, code: mfaCode, portal: "admin" }),
          })
        : await api<User>("/auth/login", {
            method: "POST",
            body: JSON.stringify({
              username,
              password,
              portal: "admin",
              ...captcha.fields,
            }),
          });

      if (user.role !== "admin" && user.role !== "super_admin") {
        await api("/auth/logout", { method: "POST" });
        setMfaChallenge("");
        setMfaCode("");
        setError("需要管理员账户");
        if (site?.auth_captcha_enabled && !mfaChallenge) void refreshCaptcha();
        return;
      }

      router.push("/admin");
      router.refresh();
    } catch (e) {
      const failure = e as Error & { code?: string; details?: Record<string, unknown> };
      if (failure.code === "mfa_required" && typeof failure.details?.challenge === "string") {
        setMfaChallenge(failure.details.challenge);
        setError("");
      } else {
        setError(failure.message);
        if (site?.auth_captcha_enabled && !mfaChallenge) void refreshCaptcha();
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      className="min-h-screen w-full flex items-center justify-center p-4 bg-cover bg-center bg-no-repeat select-none"
      style={{
        backgroundImage: "url('/assets/admin-login-bg.jpg')",
        backgroundColor: "#2e1065",
      }}
    >
      <div className="w-[400px] max-w-full flex flex-col items-center">
        {/* Top White Logo */}
        <div className="mb-5 text-center">
          <img
            src="/assets/admin-logo-white.png"
            alt="智简魔方"
            className="h-8 object-contain drop-shadow"
          />
        </div>

        {/* Floating White Card */}
        <div className="w-full bg-white rounded-[4px] px-10 py-12 shadow-2xl">
          <h2 className="text-[24px] font-normal text-[#333333] text-center mb-8">
            登录
          </h2>

          <form onSubmit={submit} className="w-full">
            {/* MFA Code Input */}
            {mfaChallenge ? (
              <div className="mb-6">
                <input
                  type="text"
                  placeholder="身份验证器验证码"
                  inputMode="text"
                  pattern="[0-9A-Fa-f]{6,10}"
                  maxLength={10}
                  autoComplete="one-time-code"
                  value={mfaCode}
                  onChange={e => setMfaCode(e.target.value.replace(/[^0-9A-Fa-f]/g, "").slice(0, 10).toUpperCase())}
                  required
                  autoFocus
                  className="w-full h-10 px-3.5 text-sm text-[#606266] bg-white border border-[#dcdfe6] rounded-[4px] placeholder-[#c0c4cc] focus:border-[#409eff] focus:outline-none transition-colors"
                />
                <p className="text-xs text-[#909399] mt-2">
                  请输入 6 位身份验证器动态码或恢复码
                </p>
              </div>
            ) : (
              <>
                {/* Account / Username Input */}
                <div className="mb-7">
                  <input
                    type="text"
                    name="username"
                    autoComplete="username"
                    placeholder="账号"
                    value={username}
                    onChange={e => setUsername(e.target.value)}
                    required
                    className="w-full h-10 px-3.5 text-sm text-[#606266] bg-white border border-[#dcdfe6] rounded-[4px] placeholder-[#c0c4cc] focus:border-[#409eff] focus:outline-none transition-colors"
                  />
                </div>

                {/* Password Input */}
                <div className="mb-7">
                  <input
                    type="password"
                    name="password"
                    autoComplete="current-password"
                    placeholder="密码"
                    value={password}
                    onChange={e => setPassword(e.target.value)}
                    required
                    className="w-full h-10 px-3.5 text-sm text-[#606266] bg-white border border-[#dcdfe6] rounded-[4px] placeholder-[#c0c4cc] focus:border-[#409eff] focus:outline-none transition-colors"
                  />
                </div>

                {site?.auth_captcha_enabled && <div className="mb-7"><CaptchaField captcha={captcha} disabled={busy} /></div>}

                {/* Auxiliary Row: Remember Password & Forgot Password */}
                <div className="flex items-center justify-between text-sm mb-9">
                  <label className="flex items-center gap-2 cursor-pointer text-[#333333] select-none text-sm">
                    <input
                      type="checkbox"
                      checked={rememberMe}
                      onChange={e => setRememberMe(e.target.checked)}
                      className="w-4 h-4 rounded text-[#007bfc] border-[#dcdfe6] focus:ring-0 cursor-pointer accent-[#007bfc]"
                    />
                    <span>记住密码</span>
                  </label>
                  <a
                    href="https://bbs.idcsmart.com/forum.php?mod=viewthread&tid=176"
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-[#2f54ea] hover:underline text-sm"
                  >
                    忘记密码?
                  </a>
                </div>
              </>
            )}

            {/* Error Message */}
            {error && (
              <div className="mb-4 text-xs text-[#f56c6c] text-center" role="alert">
                {error}
              </div>
            )}

            {/* Submit Button */}
            <button
              type="submit"
              disabled={busy || !captcha.ready}
              className="w-full h-10 bg-[#007bfc] hover:bg-[#208efd] active:bg-[#006ee0] text-white text-sm font-normal rounded-[4px] transition-colors flex items-center justify-center disabled:opacity-60 cursor-pointer shadow-sm"
            >
              {busy ? "登录中..." : mfaChallenge ? "验证并登录" : "登录"}
            </button>
          </form>
        </div>
      </div>
    </div>
  );
}
