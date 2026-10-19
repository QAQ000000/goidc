"use client";
import { AccountSecurity } from "./account-security";
import { PageHeading } from "./resource-page";
import { Workspace } from "./workspace";

export function AdminAccountView() {
  return <Workspace admin title="个人账户"><PageHeading title="个人账户" description="管理员个人安全设置与通知偏好" /><AccountSecurity /></Workspace>;
}
