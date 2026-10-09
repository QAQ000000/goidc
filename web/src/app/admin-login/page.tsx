import type { Metadata } from "next";
import { AdminLoginForm } from "@/components/admin-login-form";

export const metadata: Metadata = {
  title: "登录 - 财务系统",
  robots: { index: false, follow: false },
};

export default function Page() {
  return <AdminLoginForm />;
}
