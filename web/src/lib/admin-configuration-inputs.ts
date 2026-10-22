// Read models contain server metadata. Only declared write fields cross the API boundary.
export type SiteContentKind = "fields" | "navigation" | "links" | "groups";
const contentFields = {
  fields: ["field_key", "label", "field_type", "description", "required", "active", "sort_order"],
  navigation: ["parent_id", "label", "href", "icon", "sort_order", "open_new_tab", "active"],
  links: ["name", "url", "logo_url", "description", "sort_order", "active"],
  groups: ["parent_id", "name", "description", "sort_order", "active"],
} as const;

export function siteContentInput(kind: SiteContentKind, form: Record<string, unknown>, deleting = false) {
  const version = form.id ? { expected_version: form.version } : {};
  if (deleting && kind === "groups") return version;
  return { ...Object.fromEntries(contentFields[kind].map(key => [key, key === "sort_order" ? Number(form[key]) : form[key]])), ...version };
}

export function customerLevelInput(form: Record<string, unknown>) {
  const fields = ["name", "min_income_cents", "min_purchase_count", "min_login_count", "min_recent_login_count", "min_renewal_count", "min_recent_renewal_count", "sort_order", "active"];
  return { ...Object.fromEntries(fields.map(key => [key, form[key]])), ...(form.id ? { expected_version: form.version } : {}) };
}

export function ticketStatusInput(form: { id?: number; version?: number; slug: string; title: string; sort_order: number; active: boolean }) {
  return { title: form.title, sort_order: form.sort_order, active: form.active, ...(form.id ? { expected_version: form.version } : { slug: form.slug }) };
}
