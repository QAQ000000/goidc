export type ChannelField = {
  key: string;
  label: string;
  type: string;
  required: boolean;
  secret: boolean;
  options?: string[];
  default?: unknown;
  min?: number;
  max?: number;
  integer?: boolean;
};

export type ChannelDescriptor = {
  id: string;
  name: string;
  category: string;
  capabilities?: string[] | null;
  fields?: ChannelField[] | null;
  version?: string;
  schema_version?: number;
  contract_version?: number;
  instance_mode?: "channel" | "supplier" | "profile";
  factory_available?: boolean;
};

export type ChannelInstance = {
  id: string;
  category: string;
  implementation_id: string;
  display_name: string;
  enabled: boolean;
  sort_order: number;
  config: Record<string, unknown>;
  secret_configured: boolean;
  secure_fields?: Record<string, boolean>;
  schema_version?: number;
  factory_available?: boolean;
  configuration_status?: "valid" | "invalid" | "implementation_missing" | "schema_incompatible" | "external";
  validation_errors?: { field: string; code: string }[];
  version: number;
  created_at?: string;
  updated_at?: string;
};

export type ChannelDirectoryItem = { descriptor?: ChannelDescriptor; instance?: ChannelInstance };

const fieldTypes = new Set(["text", "number", "boolean", "select", "textarea", "multiselect"]);

export function isSupportedChannelField(field: ChannelField) {
  return fieldTypes.has(field.type);
}

export function channelSchemaVersion(descriptor: ChannelDescriptor) {
  return descriptor.schema_version ?? 1;
}

export function channelInstanceMode(descriptor: ChannelDescriptor) {
  return descriptor.instance_mode ?? (descriptor.category === "provision" ? "supplier" : "channel");
}

export function channelConfigurationPath(descriptor: ChannelDescriptor) {
  return channelInstanceMode(descriptor) === "profile" ? "/admin/products" : "/admin/suppliers";
}

export function channelSchemaCompatible(descriptor: ChannelDescriptor, instance?: ChannelInstance) {
  return !instance || (instance.schema_version ?? 1) === channelSchemaVersion(descriptor);
}

export function channelFactoryAvailable(descriptor: ChannelDescriptor, instance?: ChannelInstance) {
  return (instance?.factory_available ?? descriptor.factory_available) !== false;
}

export function channelSecureConfigured(descriptor: ChannelDescriptor, instance: ChannelInstance | undefined, key: string) {
  if (instance?.secure_fields) return instance.secure_fields[key] === true;
  const fields = (descriptor.fields ?? []).filter(field => field.secret);
  return fields.length === 1 && fields[0].key === key && instance?.secret_configured === true;
}

// Defaults become actual form data. A displayed select option must never be
// mistaken for a value that will be sent to the server.
export function initialChannelConfig(descriptor: ChannelDescriptor, config: Record<string, unknown> = {}) {
  const values = { ...config };
  for (const field of descriptor.fields ?? []) {
    if (field.secret || (values[field.key] !== undefined && values[field.key] !== null)) continue;
    if (field.default !== undefined) values[field.key] = Array.isArray(field.default) ? [...field.default] : field.default;
    else if (field.type === "boolean") values[field.key] = false;
    else if (field.type === "multiselect") values[field.key] = [];
  }
  return values;
}

export function channelDirectoryRows(descriptors: ChannelDescriptor[], instances: ChannelInstance[]): ChannelDirectoryItem[] {
  const known = new Set(descriptors.map(descriptor => descriptor.id));
  return [
    ...descriptors.flatMap<ChannelDirectoryItem>(descriptor => {
      const configured = instances.filter(instance => instance.implementation_id === descriptor.id);
      return configured.length ? configured.map(instance => ({ descriptor, instance })) : [{ descriptor }];
    }),
    ...instances.filter(instance => !known.has(instance.implementation_id)).map(instance => ({ instance })),
  ];
}

export function channelConfigurationErrors(descriptor: ChannelDescriptor, config: Record<string, unknown>, enabled: boolean, secureConfig: Record<string, string>, clearSecureFields: string[], instance?: ChannelInstance) {
  const errors: string[] = [];
  const fields = descriptor.fields ?? [];
  const known = new Map(fields.map(field => [field.key, field]));
  if (!channelSchemaCompatible(descriptor, instance)) errors.push("配置版本与当前模块不兼容，请先完成配置迁移。");
  for (const [key, value] of Object.entries(secureConfig)) {
    if (!known.get(key)?.secret) errors.push(`未知密钥字段：${key}。`);
    else if (value.length > 4096) errors.push(`${known.get(key)?.label || key}过长。`);
    else if (value !== "" && clearSecureFields.includes(key)) errors.push(`${known.get(key)?.label || key}不能同时替换和清除。`);
  }
  for (const key of clearSecureFields) if (!known.get(key)?.secret) errors.push(`未知密钥字段：${key}。`);
  if (new Set(clearSecureFields).size !== clearSecureFields.length) errors.push("清除密钥字段重复。");
  for (const key of Object.keys(config)) {
    if (!known.has(key)) errors.push(`配置包含未知字段：${key}。`);
    else if (known.get(key)?.secret) errors.push(`密钥字段 ${known.get(key)?.label} 不能放入普通配置。`);
  }
  for (const field of fields) {
    const label = field.label || field.key;
    if (!isSupportedChannelField(field)) {
      errors.push(`当前界面尚不支持 ${label} 的字段类型（${field.type}），无法保存。`);
      continue;
    }
    if (field.secret) {
      const configured = !!secureConfig[field.key] || (!clearSecureFields.includes(field.key) && channelSecureConfigured(descriptor, instance, field.key));
      if (enabled && field.required && !configured) errors.push(`启用接口前请配置${label}。`);
      continue;
    }
    const value = config[field.key];
    const missing = value === undefined || value === null || value === "";
    if (missing) {
      if (field.required) errors.push(`请填写${label}。`);
      continue;
    }
    if (field.type === "boolean") {
      if (typeof value !== "boolean") errors.push(`${label}必须为开关值。`);
    } else if (field.type === "number") {
      if (typeof value !== "number" || !Number.isFinite(value)) errors.push(`${label}必须为有效数字。`);
      else if (field.integer && !Number.isInteger(value)) errors.push(`${label}必须为整数。`);
      else if ((field.min !== undefined && value < field.min) || (field.max !== undefined && value > field.max)) errors.push(`${label}超出允许范围。`);
    } else if (field.type === "multiselect") {
      if (!Array.isArray(value) || value.some(option => typeof option !== "string" || !field.options?.includes(option)) || new Set(value).size !== value.length) errors.push(`${label}包含无效选项。`);
      else if (field.required && !value.length) errors.push(`请选择${label}。`);
    } else if (typeof value !== "string") errors.push(`${label}必须为文本。`);
    else if (field.type === "select" && !field.options?.includes(value)) errors.push(`请选择有效的${label}。`);
    else if (field.required && !value.trim()) errors.push(`请填写${label}。`);
  }
  return errors;
}

export function channelMutationBody(descriptor: ChannelDescriptor, values: { displayName: string; enabled: boolean; config: Record<string, unknown>; secureConfig: Record<string, string>; clearSecureFields: string[] }, instance?: ChannelInstance) {
  const fields = new Map((descriptor.fields ?? []).filter(field => field.secret).map(field => [field.key, field]));
  return {
    category: descriptor.category,
    implementation_id: descriptor.id,
    display_name: values.displayName,
    enabled: values.enabled,
    sort_order: instance?.sort_order ?? 0,
    config: values.config,
    schema_version: channelSchemaVersion(descriptor),
    secure_config: Object.fromEntries(Object.entries(values.secureConfig).filter(([key, value]) => fields.has(key) && value !== "")),
    clear_secure_fields: values.clearSecureFields.filter(key => fields.has(key)),
    ...(instance ? { expected_version: instance.version, expected_schema_version: instance.schema_version ?? 1 } : {}),
  };
}

export function channelDisableBody(instance: ChannelInstance) {
  return { enabled: false, expected_version: instance.version };
}
