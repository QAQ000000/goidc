import type { Metadata } from "next";
export const metadata: Metadata = { robots: { index: false, follow: false } };
import { ForgotPasswordForm } from "@/components/password-reset-form";

export default function Page() {
  return <ForgotPasswordForm />;
}
