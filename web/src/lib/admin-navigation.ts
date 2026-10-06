import { LayoutDashboard, Users, Tag, Package, ClipboardList, Server, ClipboardCheck, Receipt, MessageSquare, MessagesSquare, History, BarChart3, Mail, ShieldCheck, Settings, BookOpen, Download, FileCheck } from "lucide-react";
import type { User } from "./api";

export const adminNavigation = [
  { href: "/admin", label: "工作台", icon: LayoutDashboard, permission: "dashboard.view" },
  { href: "/admin/customers", label: "客户管理", icon: Users, permission: "customers.view" },
  { href: "/admin/customer-groups", label: "客户分组", icon: Users, permission: "customer_groups.view" },
  { href: "/admin/promo-codes", label: "优惠码", icon: Tag, permission: "promos.view" },
  { href: "/admin/products", label: "产品目录", icon: Package, permission: "products.view" },
  { href: "/admin/service-groups", label: "服务分组", icon: Server, permission: "products.view" },
  { href: "/admin/content", label: "新闻与知识库", icon: BookOpen, permission: "content.view" },
  { href: "/admin/downloads", label: "下载中心", icon: Download, permission: "downloads.view" },
  { href: "/admin/customer-files", label: "客户文件", icon: Download, permission: "customer_files.view" },
  { href: "/admin/orders", label: "订单管理", icon: ClipboardList, permission: "orders.view" },
  { href: "/admin/services", label: "服务主机", icon: Server, permission: "services.view" },
  { href: "/admin/cancellations", label: "取消申请", icon: ClipboardCheck, permission: "services.view" },
  { href: "/admin/invoices", label: "财务账单", icon: Receipt, permission: "invoices.view" },
  { href: "/admin/invoice-applications", label: "发票审核", icon: FileCheck, permission: "invoice_applications.view" },
  { href: "/admin/tickets", label: "支持工单", icon: MessageSquare, permission: "tickets.view" },
  { href: "/admin/ticket-departments", label: "工单部门", icon: Users, permission: "@super" },
  { href: "/admin/ticket-presets", label: "预设回复", icon: MessagesSquare, permission: "ticket_presets.view" },
  { href: "/admin/audit", label: "操作审计", icon: History, permission: "audit.view" },
  { href: "/admin/reports", label: "统计报表", icon: BarChart3, permission: "reports.view" },
  { href: "/admin/notifications", label: "通知管理", icon: Mail, permission: "notifications.view" },
  { href: "/admin/migrations", label: "迁移复核", icon: ClipboardCheck, permission: "migrations.view" },
  { href: "/admin/access", label: "管理员与权限", icon: ShieldCheck, permission: "@super" },
  { href: "/admin/settings", label: "站点设置", icon: Settings, permission: "@super" },
];

export function can(user: User | null, permission: string) {
  return !!user && (user.role === "super_admin" || (user.role === "admin" && permission !== "@super" && user.permissions?.includes(permission)));
}

export function adminEntry(user: User) {
  return adminNavigation.find(item => can(user, item.permission))?.href ?? "/console/account";
}
