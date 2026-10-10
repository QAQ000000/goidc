import { LayoutDashboard, Users, Tag, Package, ClipboardList, Server, ClipboardCheck, Receipt, MessageSquare, MessagesSquare, History, BarChart3, Mail, ShieldCheck, Settings, BookOpen, Download, FileCheck, Network, Plug, BadgeCheck, HandCoins, UserRoundCog } from "lucide-react";
import type { User } from "./api";

export const adminNavigation = [
  { href: "/admin", label: "工作台", icon: LayoutDashboard, permission: "dashboard.view", group: "overview" },
  { href: "/admin/customers", label: "客户列表", icon: Users, permission: "customers.view", group: "customer_mgmt" },
  { href: "/admin/customer-resources", label: "客户资源池", icon: UserRoundCog, permission: "customers.view", group: "customer_mgmt" },
  { href: "/admin/certifications", label: "实名认证", icon: BadgeCheck, permission: "certifications.view", group: "customer_mgmt" },
  { href: "/admin/customer-groups", label: "客户分组", icon: Users, permission: "customer_groups.view", group: "customer_mgmt" },
  { href: "/admin/customer-files", label: "客户文件", icon: Download, permission: "customer_files.view", group: "customer_mgmt" },
  { href: "/admin/affiliate", label: "推介计划", icon: HandCoins, permission: "affiliates.view", group: "customer_ops" },
  { href: "/admin/promo-codes", label: "优惠码", icon: Tag, permission: "promos.view", group: "finance" },
  { href: "/admin/products", label: "产品目录", icon: Package, permission: "products.view", group: "products" },
  { href: "/admin/catalog-groups", label: "目录分组", icon: Package, permission: "products.view", group: "products" },
  { href: "/admin/suppliers", label: "供应商", icon: Network, permission: "products.view", group: "products" },
  { href: "/admin/service-groups", label: "服务分组", icon: Server, permission: "products.view", group: "products" },
  { href: "/admin/content", label: "新闻与知识库", icon: BookOpen, permission: "content.view", group: "content" },
  { href: "/admin/downloads", label: "下载中心", icon: Download, permission: "downloads.view", group: "content" },
  { href: "/admin/orders", label: "订单管理", icon: ClipboardList, permission: "orders.view", group: "orders" },
  { href: "/admin/services", label: "服务主机", icon: Server, permission: "services.view", group: "products" },
  { href: "/admin/cancellations", label: "取消申请", icon: ClipboardCheck, permission: "services.view", group: "orders" },
  { href: "/admin/invoices", label: "财务账单", icon: Receipt, permission: "invoices.view", group: "finance" },
  { href: "/admin/invoice-applications", label: "发票审核", icon: FileCheck, permission: "invoice_applications.view", group: "finance" },
  { href: "/admin/tickets", label: "支持工单", icon: MessageSquare, permission: "tickets.view", group: "support" },
  { href: "/admin/ticket-departments", label: "工单部门", icon: Users, permission: "@super", group: "support" },
  { href: "/admin/ticket-presets", label: "预设回复", icon: MessagesSquare, permission: "ticket_presets.view", group: "support" },
  { href: "/admin/audit", label: "操作审计", icon: History, permission: "audit.view", group: "system" },
  { href: "/admin/reports", label: "统计报表", icon: BarChart3, permission: "reports.view", group: "reports" },
  { href: "/admin/migrations", label: "迁移复核", icon: ClipboardCheck, permission: "migrations.view", group: "system" },
  { href: "/admin/access", label: "管理员与权限", icon: ShieldCheck, permission: "@super", group: "system" },
  { href: "/admin/settings", label: "站点设置", icon: Settings, permission: "settings.view", group: "system" },
  { href: "/admin/auth-settings", label: "注册与验证码", icon: ShieldCheck, permission: "settings.view", group: "system" },
  { href: "/admin/channels", label: "渠道实例", icon: Plug, permission: "@super", group: "system" },
  { href: "/admin/notifications", label: "通知模板与队列", icon: Mail, permission: "notifications.view", group: "system" },
  { href: "/admin/system/runtime", label: "运行环境", icon: Settings, permission: "settings.view", group: "system" },
  { href: "/admin/system/database", label: "数据库状态", icon: Settings, permission: "settings.view", group: "system" },
  { href: "/admin/system/upgrade", label: "升级检查", icon: Settings, permission: "settings.view", group: "system" },
  { href: "/admin/jobs", label: "任务状态", icon: Settings, permission: "jobs.view", group: "system" },
  { href: "/admin/logs/jobs", label: "定时任务日志", icon: History, permission: "@super", group: "system" },
  { href: "/admin/jobs/queue", label: "任务队列", icon: Settings, permission: "jobs.view", group: "system" },
  { href: "/admin/system/about", label: "关于系统", icon: Settings, permission: "settings.view", group: "system" },
  { href: "/admin/channels", label: "接口设置", icon: Plug, permission: "@super", group: "system" },
];

export const adminNavigationGroups = [
  { id: "overview", label: "概览" },
  { id: "customer_mgmt", label: "客户管理" },
  { id: "customer_ops", label: "运营管理" },
  { id: "products", label: "商品与服务" },
  { id: "orders", label: "订单" },
  { id: "finance", label: "财务" },
  { id: "support", label: "工单与通知" },
  { id: "content", label: "内容与资料" },
  { id: "reports", label: "报表" },
  { id: "system", label: "系统" },
];

export const adminDomains = [
  { id: "overview", label: "工作台", href: "/admin", groups: ["overview"] },
  { id: "customers", label: "客户", href: "/admin/customers", groups: ["customer_mgmt", "customer_ops"] },
  { id: "business", label: "业务", href: "/admin/orders", groups: ["orders", "products"] },
  { id: "finance", label: "财务", href: "/admin/invoices", groups: ["finance", "reports"] },
  { id: "support", label: "工单", href: "/admin/tickets", groups: ["support"] },
  { id: "content", label: "功能", href: "/admin/content", groups: ["content"] },
  { id: "system", label: "设置", href: "/admin/settings", groups: ["system"] },
];

export function adminDomain(path: string) {
  const group = adminNavigationItem(path)?.group;
  return adminDomains.find(domain => domain.groups.includes(group ?? "")) ?? adminDomains[0];
}

export function adminNavigationItem(path: string) {
  return [...adminNavigation].sort((a, b) => b.href.length - a.href.length).find(item => path === item.href || (item.href !== "/admin" && path.startsWith(`${item.href}/`)));
}

export function can(user: User | null, permission: string) {
  return !!user && (user.role === "super_admin" || (user.role === "admin" && permission !== "@super" && user.permissions?.includes(permission)));
}

export function adminEntry(user: User) {
  return adminNavigation.find(item => can(user, item.permission))?.href ?? "/console/account";
}
