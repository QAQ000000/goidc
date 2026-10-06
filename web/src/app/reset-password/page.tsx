import type { Metadata } from "next";
export const metadata: Metadata = { robots: { index: false, follow: false } };
import { Suspense } from "react";
import { ResetPasswordForm } from "@/components/password-reset-form";

export default function Page() {
  return <Suspense fallback={<main className="min-h-screen grid place-items-center">加载中…</main>}><ResetPasswordForm /></Suspense>;
}
