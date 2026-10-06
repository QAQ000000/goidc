export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public details: Record<string, unknown> = {},
  ) {
    super(message);
  }
}
function noticeMaintenance(code: string | undefined) {
  if (code === "site_maintenance" && typeof window !== "undefined") window.dispatchEvent(new Event("zjmfgo:maintenance"));
}
export async function api<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    ...options,
    credentials: "same-origin",
    cache: "no-store",
    headers: {
      "Content-Type": "application/json",
      "X-Requested-With": "zjmfgo",
      ...options.headers,
    },
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    noticeMaintenance(body.error?.code);
    throw new ApiError(
      response.status,
      body.error?.code ?? "request_failed",
      body.error?.message ?? "请求失败，请稍后重试",
      body.error ?? {},
    );
  }
  return body.data as T;
}
export async function upload<T>(path: string, form: FormData, method: "POST" | "PUT" = "POST"): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    method,
    body: form,
    credentials: "same-origin",
    cache: "no-store",
    headers: { "X-Requested-With": "zjmfgo" },
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    noticeMaintenance(body.error?.code);
    throw new ApiError(
      response.status,
      body.error?.code ?? "request_failed",
      body.error?.message ?? "上传失败，请稍后重试",
    );
  }
  return body.data as T;
}
export async function download(path: string): Promise<Blob> {
  const response = await fetch(`/api/v1${path}`, {
    credentials: "same-origin",
    cache: "no-store",
    headers: { "X-Requested-With": "zjmfgo" },
  });
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    noticeMaintenance(body.error?.code);
    throw new ApiError(
      response.status,
      body.error?.code ?? "request_failed",
      body.error?.message ?? "下载失败，请稍后重试",
    );
  }
  return response.blob();
}
export function money(cents: number) {
  return new Intl.NumberFormat("zh-CN", {
    style: "currency",
    currency: "CNY",
  }).format(cents / 100);
}
export function date(value: string) {
  return new Intl.DateTimeFormat("zh-CN", {
    dateStyle: "medium",
    timeStyle: "short",
    timeZone: "Asia/Shanghai",
  }).format(new Date(value));
}
export type User = {
  id: number;
  username: string;
  email: string;
  display_name: string;
  role: string;
  permissions: string[];
  ticket_scope?: "all" | "departments";
  ticket_unassigned?: boolean;
  customer_scope?: "all" | "owned" | "assigned";
  customer_ownerless?: boolean;
  sales_enabled?: boolean;
};
export type SiteProfile = {
  site_name: string;
  description: string;
  company_name: string;
  logo_url: string;
  support_email: string;
  support_phone: string;
  address: string;
  support_url: string;
  terms_url: string;
  privacy_url: string;
  record_number: string;
  copyright: string;
  registration_enabled: boolean;
  maintenance_enabled: boolean;
  maintenance_message: string;
  maintenance_url: string;
};
export type SiteSettings = SiteProfile & { version: number; updated_at: string };
export type ApiKey = {
  id: number;
  name: string;
  prefix: string;
  last_used_at: string | null;
  expires_at: string | null;
  revoked_at: string | null;
  created_at: string;
};
export type Contact = {
  id: number;
  customer_id: number;
  username: string;
  sex: number;
  avatar: string;
  company_name: string;
  email: string;
  wechat_id: string;
  country: string;
  province: string;
  city: string;
  region: string;
  address1: string;
  address2: string;
  postcode: string;
  phone_number: string;
  permissions: string[];
  general_emails: boolean;
  invoice_emails: boolean;
  product_emails: boolean;
  support_emails: boolean;
  status: "active" | "inactive" | "disabled";
  created_at: string;
  updated_at: string;
};
export type CustomerGroup = {
  id: number;
  name: string;
  color: string;
  discount_percent: number;
  suspend_delete_exempt: boolean;
  separate_invoices: boolean;
  member_count: number;
};
export type PromoCode = {
  id: number;
  code: string;
  discount_type: "percent" | "fixed" | "free";
  value: number;
  cycles: string[];
  applies_product_ids: number[];
  requires_product_ids: number[];
  requires_existing: boolean;
  starts_at: string | null;
  expires_at: string | null;
  max_uses: number;
  used_count: number;
  one_time: boolean;
  only_new_customer: boolean;
  only_old_customer: boolean;
  once_per_customer: boolean;
  recurring: boolean;
  recurring_limit: number;
  upgrade_only: boolean;
  active: boolean;
  notes: string;
  created_at: string;
  updated_at: string;
};
export type ContentArticle = {
  id: number;
  content_type: "news" | "knowledge";
  category_id: number | null;
  category_name: string | null;
  title: string;
  slug: string;
  excerpt: string;
  body: string;
  published_at: string | null;
  views: number;
  created_at: string;
  updated_at: string;
};
export type AdminContentArticle = ContentArticle & { status: "draft" | "published" | "archived"; access_level: "public" | "login" | "active_service"; version: number; author_id: number | null; scheduled: boolean | null };
export type ContentCategory = { id: number; content_type: "news" | "knowledge"; name: string; slug: string; description: string; sort_order: number; active: boolean; version: number; article_count: number };
export type DownloadItem = { id:number;category_id:number|null;category_name:string|null;category_path?:string[]|null;title:string;description:string;access_level:"public"|"login"|"product";source_type:"local"|"remote";filename:string;size_bytes:number;sha256:string;downloads:number;created_at:string;updated_at:string;can_download:boolean };
export type AdminDownload = DownloadItem & { active:boolean;version:number;remote_url:string;product_ids:number[] };
export type DownloadCategory = { id:number;parent_id:number|null;name:string;description:string;sort_order:number;active:boolean;effective_active:boolean;version:number;file_count:number;direct_file_count:number;child_count:number;path:string[];depth:number };
export type DownloadSettings = { enabled:boolean;version:number };
export type CustomerFile = {id:number;customer_id:number;title:string;remarks:string;filename:string;mime_type:string;size_bytes:number;sha256:string;downloads:number;created_at:string;updated_at:string};
export type AdminCustomerFile = CustomerFile & {version:number;uploader_id:number;uploader_name:string;customer_name:string;customer_username:string};
export async function fetchDownload(item: Pick<DownloadItem,"id"|"filename">, path = `/downloads/${item.id}/file`) {
  const response = await fetch(`/api/v1${path}`, { credentials:"same-origin",cache:"no-store",headers:{"X-Requested-With":"zjmfgo"} });
  if (!response.ok) {
    const body = await response.json().catch(() => ({})); noticeMaintenance(body.error?.code);
    throw new ApiError(response.status,body.error?.code ?? "download_failed",body.error?.message ?? "下载失败");
  }
  if (response.headers.get("Content-Type")?.includes("application/json")) {
    const body = await response.json(); const target = new URL(body.data.url);
    if (!["http:","https:"].includes(target.protocol) || target.username || target.password) throw new Error("外部下载地址无效");
    window.location.assign(target.href); return;
  }
  const blob = await response.blob(); const href = URL.createObjectURL(blob); const link = document.createElement("a");
  const encodedName = response.headers.get("Content-Disposition")?.match(/filename\*=UTF-8''([^;]+)/i)?.[1];
  let filename = item.filename || "download";
  if (encodedName) { try { filename = decodeURIComponent(encodedName); } catch {} }
  link.href=href; link.download=filename; document.body.appendChild(link); link.click(); link.remove();
  window.setTimeout(() => URL.revokeObjectURL(href),1000);
}
export type CatalogSection = { id: number; name: string; slug: string; description: string; sort_order: number; active: boolean; version: number; member_count: number };
export type CatalogCategory = CatalogSection & { section_id: number | null; section_name: string | null; section_active: boolean; headline: string; tagline: string };
export type ProductPage = { items: Product[]; total: number; limit: number; offset: number };
export type PurchaseField = { key:string;label:string;type:"text"|"textarea"|"select"|"checkbox"|"url";description:string;required:boolean;active:boolean;show_invoice:boolean;max_length:number;pattern:string;choices:string[];sort_order:number };
export type PurchaseFieldSnapshot = { key:string;label:string;type:string;value:string;show_invoice:boolean };
export type NOKVMResources = {
  areas_id:number;core:number;cpu_mode:string;memory:number;data_disk_size:number;
  net_out:number;net_in:number;snapshoot:number;backups:number;templates_id:number;
  ip_num:number;flow_limit:number;nat_acl_limit:number;nat_web_limit:number;nodes_id:number;
  login:"root"|"administrator";
};
export type NOKVMTemplate={id:number;name:string;group:string;login:"root"|"administrator"};
export type NOKVMMonitoring={
 start:string;end:string;fetched_at:string;usage_available:boolean;
 usage:{in_bytes:string;out_bytes:string;total_bytes:string;limit_bytes:string|null}|null;
 samples:{at:string;cpu_percent:number|null;network_in_kbps:number|null;network_out_kbps:number|null;disks:Record<string,{read_kbps:number|null;write_kbps:number|null}>}[];
};
export type DeliveryConfig={profile_id:string;resources:NOKVMResources;templates?:NOKVMTemplate[]};
export type Product = {
  delivery_config?:DeliveryConfig|null;
  purchase_field_definitions?: PurchaseField[];
	config_upgrade_enabled?: boolean;
	config_downgrade_enabled?: boolean;
  id: number;
  service_group_id?: number | null;
  service_group_name?: string | null;
	category_id: number | null;
	category_name: string | null;
	section_id: number | null;
	category_visible: boolean;
	sort_order: number;
	revision: number;
  name: string;
  slug: string;
  description: string;
  billing_cycle: string;
  provision_mode: "manual" | "simulation" | "nokvm";
  approval_required: boolean;
  price_cents: number;
  setup_fee_cents: number;
  stock: number | null;
  active: boolean;
  pricing?: {
    id: number;
    billing_cycle: string;
    price_cents: number;
    setup_fee_cents: number;
    active: boolean;
  }[];
  option_groups?: ProductOptionGroup[];
};
export type ProductOptionPrice = { id:number;billing_cycle:string;price_cents:number;setup_fee_cents:number;active:boolean };
export type ProductOptionPriceTier = { id?:number;billing_cycle:string;qty_minimum:number;qty_maximum:number;price_cents:number;setup_fee_cents:number;active?:boolean };
export type ProductOption = { id:number;parent_option_id?:number;label:string;value:string;price_delta_cents:number;setup_delta_cents:number;sort_order:number;active:boolean;hidden?:boolean;qty_minimum?:number;qty_maximum?:number;upgrade_enabled?:boolean;pricing?:ProductOptionPrice[];price_tiers?:ProductOptionPriceTier[] };
export type ProductOptionGroup = { id:number;parent_group_id?:number;name:string;slug:string;required:boolean;active:boolean;sort_order:number;option_type?:number;qty_minimum?:number;qty_maximum?:number;qty_stage?:number;unit?:string;upgrade_enabled?:boolean;options:ProductOption[] };
export type OptionSnapshot = { id:number;group_id:number;group_name:string;group_slug:string;label:string;value:string;quantity?:number;price_delta_cents:number;setup_delta_cents:number };
export type CartItem = Product & {
  purchase_fields: Record<string,string>;
  purchase_field_snapshot: PurchaseFieldSnapshot[];
  fields_valid: boolean;
  fields_error?: string;
  product_id: number;
  quantity: number;
  option_ids: number[];
  option_quantities: Record<string, number>;
  current: boolean;
  total_cents: number;
  options_valid:boolean;
  config_snapshot:OptionSnapshot[];
};
export type Cart = {
  id: string;
  version: number;
  currency: string;
  items: CartItem[];
  discount_cents: number;
  promo?: {
    recurring?: boolean;
    recurring_limit?: number;
    id: number;
    code: string;
    discount_type?: string;
    discount_cents: number;
    subtotal_cents?: number;
    total_cents?: number;
    error?: string;
  } | null;
};
export type Ticket = {
  admin_access: boolean;
  id: number;
  ticket_no: string;
  legacy_ticket_no?: string;
  legacy_status_label?: string;
  service_id?: number | null;
  user_id?: number;
  customer_name?: string;
  subject: string;
  body: string;
  priority: string;
  status: string;
  version: number;
  department_id: number | null;
  department_name: string | null;
  assignee_id: number | null;
  assignee_name: string | null;
  merged_ticket_id?: number | null;
  updated_at: string;
  replies: {
    id: number;
    body: string;
    created_at: string;
    author_name: string;
    author_role: string;
  }[];
  attachments: TicketAttachment[];
  notes?: TicketNote[];
};
export type TicketNote = {
  id: number;
  ticket_id: number;
  author_id: number;
  author_name: string;
  body: string;
  created_at: string;
};
export type TicketReplyPreset = {
  id: number;
  category_id: number;
  category_name: string;
  category_active: boolean;
  title: string;
  body: string;
  active: boolean;
  created_at: string;
  updated_at: string;
};
export type TicketReplyPresetCategory = {
  id: number;
  name: string;
  sort_order: number;
  active: boolean;
  created_at: string;
  updated_at: string;
};
export type TicketAttachment = {
  id: number;
  ticket_id: number;
  reply_id?: number | null;
  note_id?: number | null;
  uploader_id: number;
  uploader_name?: string;
  filename: string;
  mime_type: string;
  size_bytes: number;
  sha256: string;
  created_at: string;
};
export type TicketDepartment = {
  id: number;
  name: string;
  slug: string;
  sort_order?: number;
  active?: boolean;
  customer_visible?: boolean;
  version?: number;
  ticket_count?: number;
  member_count?: number;
};
export type TicketAssignee = {
  id: number;
  username: string;
  display_name: string;
  role: string;
};
export type ServiceGroup = { id:number;name:string;icon:string;sort_order:number;version:number;product_count?:number };
export type ServiceNavigationGroup = ServiceGroup & { visible:boolean;preference_version:number;service_count:number;active_count:number };
export type ServicePage = { items:Service[];total:number;limit:number;offset:number };
export type NOKVMStoragePoint={id:string;name:string;created_at:string};
export type NOKVMStorageListing={kind:"snapshot"|"backup";limit:number;items:NOKVMStoragePoint[];fetched_at:string};
export type NOKVMCDROMImage={id:string;name:string;description:string};
export type NOKVMCDROMListing={items:NOKVMCDROMImage[];mounted_id:string;fetched_at:string};
export type Service = {
  installed_os?:Partial<NOKVMTemplate>;
  purchase_fields?: PurchaseFieldSnapshot[];
	product_revision?: number;
	product_upgrade_available?: boolean;
	config_upgrade_enabled: boolean;
	config_downgrade_enabled: boolean;
	recurring_price_cents: number;
	renewal_amount_cents: number;
	renewal_discount_percent: number;
	renewal_fixed_discount_cents: number;
	renewal_discount_limit: number;
	renewal_discounts_used: number;
	renewal_promo_code: string;
	config_snapshot: OptionSnapshot[];
	option_ids: number[];
	option_quantities: Record<number, number>;
	configuration_changes?: ConfigurationChange[];
  id: number;
  service_group_id: number | null;
  service_group_name: string | null;
  user_id: number;
  order_id: number;
  product_id: number;
  name: string;
  domain: string;
  billing_cycle: string;
  provision_mode: "manual" | "simulation" | "nokvm";
  resource_info?:{upstream_id?:string;name?:string;ips?:string[];power?:string};
  unit_number: number;
  inventory_released: boolean;
  status: string;
  next_due_at: string | null;
  auto_renew: boolean;
  auto_renew_error: string;
  username: string;
  upstream_id: string;
  last_error: string;
  version: number;
  customer_name?: string;
  created_at: string;
  updated_at: string;
  jobs?: {
    request_payload?:Partial<NOKVMTemplate> & {kind?:"snapshot"|"backup";operation?:string;point_id?:string;point?:NOKVMStoragePoint;limit?:number};
    id: number;
    action: string;
    status: string;
    attempts: number;
    available_at: string;
    locked_at: string | null;
    completed_at: string | null;
    last_error: string;
    created_at: string;
  }[];
  cancellation?: CancellationRequest | null;
};
export type ConfigurationChange = {
	change_type?:"configuration"|"product"|"billing_cycle";target_product_id?:number|null;target_product_name?:string|null;
  id:number;service_id:number;amount_cents:number;credit_cents:number;
	old_recurring_cents:number;new_recurring_cents:number;product_revision:number;service_version:number;billing_cycle?:string;new_billing_cycle?:string;
	upgrade_base_cents:number;upgrade_discount_cents:number;upgrade_promo_code:string;
  due_at:string;expires_at:string;config_snapshot:OptionSnapshot[];status:string;
  invoice_id:number|null;job_id:number|null;created_at?:string;completed_at?:string|null;
};
export type CancellationRequest = {
  id: number;
  service_id: number;
  user_id: number;
  type: "immediate" | "end_of_billing";
  reason: string;
  status: string;
  resolution_note: string;
  created_at: string;
  resolved_at?: string | null;
  resolved_by?: number | null;
  job_id?: number;
  replayed?: boolean;
};
