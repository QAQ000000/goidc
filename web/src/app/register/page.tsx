import type { Metadata } from "next";
export const metadata: Metadata = { robots: { index: false, follow: false } };
import { AuthForm } from "@/components/auth-form";
export default function Page() {
  return <AuthForm register />;
}
