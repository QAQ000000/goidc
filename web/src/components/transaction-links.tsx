"use client";

import Link from "next/link";
import { date } from "@/lib/api";
import { useAdminPermission } from "./workspace";

export function time(value: string | null | undefined) {
  return value ? date(value) : "-";
}

function useOrderLink(admin: boolean) {
  const allowed = useAdminPermission("orders.view");
  return !admin || allowed;
}

export function OrderLink({
  admin,
  id,
  label,
}: {
  admin: boolean;
  id: number;
  label: string;
}) {
  const allowed = useOrderLink(admin);
  return allowed ? (
    <Link
      className="text-primary hover:underline break-all"
      href={`${admin ? "/admin" : "/console"}/orders/${id}`}
    >
      {label}
    </Link>
  ) : (
    <span className="break-all">{label}</span>
  );
}

export function ServiceLink({
  admin,
  id,
  label,
}: {
  admin: boolean;
  id: number;
  label: string;
}) {
  const allowed = useAdminPermission("services.view");
  return !admin || allowed ? (
    <Link
      className="text-primary hover:underline break-all"
      href={`${admin ? "/admin" : "/console"}/services/${id}`}
    >
      {label}
    </Link>
  ) : (
    <span>{label}</span>
  );
}
