import { downloadFilename, saveBlob } from "./download";

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public details: Record<string, unknown> = {},
    public correlationId?: string,
  ) {
    super(correlationId && status >= 500 ? `${message}（错误编号：${correlationId}）` : message);
  }
}
function errorCorrelation(response: Response, body: { error?: Record<string, unknown> }): string | undefined {
  const value = response.headers.get("X-Request-ID") ?? body.error?.correlation_id;
  return typeof value === "string" && /^[a-f0-9]{32}$/.test(value) ? value : undefined;
}
function noticeMaintenance(code: string | undefined) {
  if (code === "site_maintenance" && typeof window !== "undefined") window.dispatchEvent(new Event("zjmfgo:maintenance"));
}
// 登录、注册、验证码等端点自身的 401 表示凭据错误，不能触发全局跳转，否则会在登录页形成循环。
const credentialEndpoints = ["/auth/login", "/auth/login/mfa", "/auth/register", "/auth/captcha", "/auth/verification/", "/auth/password-reset/"];
function isCredentialEndpoint(path: string) {
  const clean = path.split("?")[0];
  return credentialEndpoints.some((prefix) => clean === prefix || clean.startsWith(prefix));
}
// 会话过期时派发全局事件，由壳层组件统一跳转到对应登录页。
function noticeUnauthorized(path: string, status: number) {
  if (status !== 401 || typeof window === "undefined" || isCredentialEndpoint(path)) return;
  window.dispatchEvent(new CustomEvent("zjmfgo:unauthorized", { detail: { path } }));
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
    noticeUnauthorized(path, response.status);
    throw new ApiError(
      response.status,
      body.error?.code ?? "request_failed",
      body.error?.message ?? "请求失败，请稍后重试",
      body.error ?? {},
      errorCorrelation(response, body),
    );
  }
  return body.data as T;
}
export type CaptchaPresentation = {
  kind: "image" | "click" | "widget";
  renderer?: string;
  image?: string;
  width?: number;
  height?: number;
  prompt?: string;
  max_points?: number;
  public_data?: Record<string, unknown>;
};
export type CaptchaProof =
  | { kind: "image"; answer: string }
  | { kind: "click"; points: { x: number; y: number }[] }
  | { kind: "widget"; token: string };
export type CaptchaChallenge = { enabled: boolean; captcha_id?: string; image?: string; expires_at?: string; challenge?: CaptchaPresentation };
export function fetchCaptcha(action: "login" | "register" | "forgot-password", signal?: AbortSignal) {
  return api<CaptchaChallenge>(`/auth/captcha?action=${action}`, { signal });
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
    noticeUnauthorized(path, response.status);
    throw new ApiError(
      response.status,
      body.error?.code ?? "request_failed",
      body.error?.message ?? "上传失败，请稍后重试",
      body.error ?? {},
      errorCorrelation(response, body),
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
    noticeUnauthorized(path, response.status);
    throw new ApiError(
      response.status,
      body.error?.code ?? "request_failed",
      body.error?.message ?? "下载失败，请稍后重试",
      body.error ?? {},
      errorCorrelation(response, body),
    );
  }
  return response.blob();
}
// 金额以分为单位；非有限数值时返回占位符而不是 "¥NaN"。
export function money(cents: number) {
  if (!Number.isFinite(cents)) return "-";
  return new Intl.NumberFormat("zh-CN", {
    style: "currency",
    currency: "CNY",
  }).format(cents / 100);
}
// 后端可空时间戳经 String() 会变成 ""/"null"/"undefined"，不能直接交给 Date 解析。
// 这些是预期内的空值，返回占位符；只有真正非法的字符串才额外输出警告便于排查。
export function date(value: string) {
  if (!value || value === "null" || value === "undefined") return "-";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    console.warn("[zjmfgo] invalid date value:", value);
    return "-";
  }
  return new Intl.DateTimeFormat("zh-CN", {
    dateStyle: "medium",
    timeStyle: "short",
    timeZone: "Asia/Shanghai",
  }).format(parsed);
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
  email_verification_required: boolean;
  maintenance_enabled: boolean;
  maintenance_message: string;
  maintenance_url: string;
  auth_captcha_enabled: boolean;
};
// Public display settings are separate from the administrative write DTO.
export type SiteTheme = {
  template: "default" | "classic" | "minimal";
  primary_color: string;
  logo_variant: "full" | "compact" | "text";
  footer_text: string;
};
export type PublicSite = SiteProfile & { theme?: SiteTheme };
export type SiteAdminSettings = {
  default_language: "zh_CN" | "en_US";
  language_menu_enabled: boolean;
  records_per_page: 10 | 20 | 50 | 100;
  frontend_credit_enabled: boolean;
  custom_customer_id_enabled: boolean;
  admin_login_days: number;
  frontend_ip_check: boolean;
  admin_ip_check: boolean;
  debug_enabled: boolean;
};
export type SiteSettings = SiteProfile & { admin_settings: SiteAdminSettings; version: number; updated_at: string };
export type PaymentSettings = {
  enabled: boolean;
  mock_enabled: boolean;
  api_url: string;
  pid: string;
  types: string[];
  key_configured: boolean;
  configured: boolean;
  encryption_configured: boolean;
  version: number;
  updated_at: string;
};
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
  version: number;
};
export type CustomerProductGroup = {
  id: number;
  name: string;
  version: number;
  product_count: number;
  product_ids: number[];
  products: { id: number; name: string }[];
  created_at: string;
  updated_at: string;
};
export type CustomerProductDiscount = {
  id: number | null;
  customer_group_id: number;
  customer_group_name: string;
  product_group_id: number;
  product_group_name: string;
  discount_type: "none" | "percent" | "fixed";
  discount_percent: number;
  fixed_discount_cents: number;
  version: number;
};
export type PromoCode = {
  version: number;
  status?: "active" | "expired" | "disabled" | "scheduled" | "exhausted";
  id: number;
  code: string;
  discount_type: "percent" | "fixed" | "free";
  value: number;
  cycles: string[];
  applies_product_ids: number[];
  applies_customer_group_ids?: number[];
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
  categories: { id: number; name: string; slug: string }[];
  tags: { id: number; name: string; slug: string }[];
  sort_order: number;
  title: string;
  slug: string;
  excerpt: string;
  body: string;
  body_html: string;
  seo_title: string;
  seo_description: string;
  seo_canonical: string;
  published_at: string | null;
  views: number;
  created_at: string;
  updated_at: string;
};
export type AdminContentArticle = ContentArticle & { status: "draft" | "published" | "archived"; access_level: "public" | "login" | "active_service"; version: number; author_id: number | null; scheduled: boolean | null };
export type ContentCategory = { id: number; content_type: "news" | "knowledge"; name: string; slug: string; description: string; sort_order: number; active: boolean; version: number; article_count: number };
export type ContentTag = { id: number; content_type: "news" | "knowledge"; name: string; slug: string; sort_order: number; active: boolean; version: number; article_count: number };
export type DownloadItem = { id:number;category_id:number|null;category_name:string|null;category_path?:string[]|null;title:string;description:string;access_level:"public"|"login"|"product";source_type:"local"|"remote";filename:string;size_bytes:number;sha256:string;downloads:number;created_at:string;updated_at:string;can_download:boolean };
export type AdminDownload = DownloadItem & { active:boolean;version:number;remote_url:string;product_ids:number[] };
export type DownloadCategory = { id:number;parent_id:number|null;name:string;description:string;sort_order:number;active:boolean;effective_active:boolean;version:number;file_count:number;direct_file_count:number;child_count:number;path:string[];depth:number };
export type DownloadSettings = { enabled:boolean;version:number };
export type CustomerFile = {id:number;customer_id:number;title:string;remarks:string;filename:string;mime_type:string;size_bytes:number;sha256:string;downloads:number;created_at:string;updated_at:string};
export type AdminCustomerFile = CustomerFile & {version:number;uploader_id:number;uploader_name:string;customer_name:string;customer_username:string};
export async function fetchDownload(item: Pick<DownloadItem,"id"|"filename">, path = `/downloads/${item.id}/file`) {
  const response = await fetch(`/api/v1${path}`, { credentials:"same-origin",cache:"no-store",headers:{"X-Requested-With":"zjmfgo"} });
  if (!response.ok) {
    const body = await response.json().catch(() => ({})); noticeMaintenance(body.error?.code); noticeUnauthorized(path, response.status);
    throw new ApiError(response.status,body.error?.code ?? "download_failed",body.error?.message ?? "下载失败",body.error ?? {},errorCorrelation(response,body));
  }
  if (response.headers.get("Content-Type")?.includes("application/json")) {
    const body = await response.json(); const target = new URL(body.data.url);
    if (!["http:","https:"].includes(target.protocol) || target.username || target.password) throw new Error("外部下载地址无效");
    window.location.assign(target.href); return;
  }
  const blob = await response.blob();
  saveBlob(blob, downloadFilename(response.headers.get("Content-Disposition"), item.filename));
}
export type CatalogSection = { id: number; name: string; slug: string; description: string; sort_order: number; active: boolean; version: number; member_count: number };
export type CatalogCategory = CatalogSection & { section_id: number | null; section_name: string | null; section_active: boolean; headline: string; tagline: string };
export type ProductPage = { items: Product[]; total: number; limit: number; offset: number };
export type PurchaseField = { key:string;label:string;type:"text"|"textarea"|"select"|"checkbox"|"url";description:string;required:boolean;active:boolean;show_invoice:boolean;max_length:number;pattern:string;choices:string[];sort_order:number;scope?:"purchase"|"admin"|"customer";encrypted?:boolean;customer_profile_key?:string };
export type PurchaseFieldSnapshot = { key:string;label:string;type:string;value:string;show_invoice:boolean;scope?:string;encrypted?:boolean;customer_profile_key?:string };
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
 supplier_catalog_mode?:"configurable"|"";
 require_verified_phone?:boolean;
 require_certification?:boolean;
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
  provision_mode: "manual" | "simulation" | "nokvm" | "supplier";
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
  configuration_rules?: NativeConfigurationRule[];
};
export type NativeRulePredicate = {group_id:number;option_id:number|null;operator:"equals"|"not_equals"|"absent"|"quantity_in"|"quantity_out";qty_minimum:number|null;qty_maximum:number|null};
export type SupplierProvider = { id:string; name:string; capabilities:{catalog:boolean;product:boolean;create:boolean;sync:boolean;power:boolean;reset_password:boolean;rescue:boolean;vnc:boolean;reinstall:boolean;suspend:boolean;resume:boolean;terminate:boolean;renew:boolean}; descriptor?:import("./channel-configuration").ChannelDescriptor };
export type SupplierAccount = { id:number; name:string; provider:string; base_url:string; username:string; active:boolean; version:number; credentials_configured:boolean; product_count:number; blocked_job_id?:number|null; running_job_id?:number|null; config?:Record<string,unknown>; secure_fields?:Record<string,boolean>; schema_version?:number; factory_available?:boolean; configuration_status?:"valid"|"invalid"|"implementation_missing"|"schema_incompatible"; configuration_errors?:{field:string;code:string}[] };
export type SupplierPrice = {amount_cents:number;setup_cents:number;raw_amount_cents?:number;raw_setup_cents?:number};
export type SupplierPriceTier = SupplierPrice & {min_qty:number;max_qty:number};
export type SupplierProduct = {excluded_cycles?:{cycle:string;reason:string}[];id:number;name:string;description:string;group_id?:number;group_name?:string;type?:string;currency?:string;revision?:string;stock?:number|null;price_multiplier?:number;prices?:Record<string,SupplierPrice>|null;options?:{id:number;name:string;type:string;required:boolean;quantity_mode?:string;choices?:{id:number;name:string;min_qty?:number;max_qty?:number;prices?:Record<string,SupplierPrice>|null;price_tiers?:Record<string,SupplierPriceTier[]>}[]|null}[]|null;fields?:{id:number;name:string;type:string;description:string;required:boolean;choices?:string[]|null}[]|null;supported:boolean;unsupported_reason?:string};
export type SupplierBatchImportInput = {expected_supplier_version:number;category_id:number|null;price_multiplier_bps:number;active:boolean;items:{upstream_product_id:number;expected_upstream_revision?:string;name?:string}[]};
export type SupplierBatchImportPreview = {expected_supplier_version:number;price_multiplier_bps:number;category_id:number|null;active:boolean;items:{product:SupplierProduct;slug:string;local_prices:Record<string,SupplierPrice>;importable:boolean;reason?:string}[]};
export type SupplierBatchImportResult = {items:{upstream_product_id:number;product_id:number;name:string;slug:string;status:"created"|"existing"}[]};
export type SupplierBinding = {id:number;supplier_id:number;provider:string;upstream_product_id:number;upstream_revision:string;billing_cycle:string;maximum_cost_cents:number;config_options:Record<string,string>;currency:string;custom_fields_configured:boolean;catalog_mode?:string;billing_cycles?:string[]};
export type NativeConfigurationRule = {id:number;name:string;enabled:boolean;conditions:NativeRulePredicate[];results:NativeRulePredicate[]};
export type ProductOptionPrice = { id:number;billing_cycle:string;price_cents:number;setup_fee_cents:number;active:boolean };
export type ProductOptionPriceTier = { pricing_mode?:"graduated"|"volume";id?:number;billing_cycle:string;qty_minimum:number;qty_maximum:number;price_cents:number;setup_fee_cents:number;active?:boolean };
export type ProductOption = { id:number;parent_option_id?:number;label:string;value:string;price_delta_cents:number;setup_delta_cents:number;sort_order:number;active:boolean;hidden?:boolean;qty_minimum?:number;qty_maximum?:number;allow_zero_quantity?:boolean;upgrade_enabled?:boolean;pricing?:ProductOptionPrice[];price_tiers?:ProductOptionPriceTier[] };
export type ProductOptionGroup = { id:number;parent_group_id?:number;name:string;slug:string;required:boolean;active:boolean;sort_order:number;option_type?:number;qty_minimum?:number;qty_maximum?:number;allow_zero_quantity?:boolean;qty_stage?:number;unit?:string;upgrade_enabled?:boolean;options:ProductOption[] };
export type OptionSnapshot = { id:number;group_id:number;group_name:string;group_slug:string;label:string;value:string;quantity?:number;price_delta_cents:number;setup_delta_cents:number };
export type CartItem = Product & {
 admission_error?:string;
 admission_code?:string;
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
export type TicketRating = { stars: number | null; provenance?: "native" | "legacy" | null; can_rate: boolean };
export type Ticket = {
  initial_rating?: TicketRating;
  created_at: string;
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
    rating?: TicketRating;
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
export type NOKVMNATItem={id:string;name:string;type:string;domain:string;exterior_port:number;interior_port:number};
export type NOKVMNATListing={kind:"acl"|"web";limit:number;items:NOKVMNATItem[];fetched_at:string};
export type NOKVMCDROMImage={id:string;name:string;description:string};
export type NOKVMCDROMListing={items:NOKVMCDROMImage[];mounted_id:string;fetched_at:string};
export type OrderRefundReversal = { order_id: number; status: "pending" | "released" | "applied"; invoice_version: number; order_version: number; service_count: number; stock_units_released: number; created_at: string; applied_at: string | null; released_at: string | null };
export type Service = {
  external_refund_order_hold?: OrderRefundReversal | null;
  product_name?:string;
  order_no?:string;
  first_price_cents?:number;
  setup_fee_cents?:number;
  invoice_id?:number|null;
  invoice_no?:string|null;
  invoice_status?:string|null;
  paid_at?:string|null;
  supplier_name?:string|null;
  supplier_provider?:string|null;
  supplier_capabilities?:SupplierProvider["capabilities"];
  upstream_product_id?:number|null;
  supplier_running_job_id?:number|null;
  supplier_blocking_service_id?:number|null;
  supplier_blocking_action?:string|null;
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
  provision_mode: "manual" | "simulation" | "nokvm" | "supplier";
  resource_info?:{upstream_id?:string;name?:string;ips?:string[];power?:string;dedicated_ip?:string;assigned_ips?:string;port?:number;suspend_reason?:string};
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
    manual_lifecycle?: boolean;
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
