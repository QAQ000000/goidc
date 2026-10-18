import type { SupplierAccount, SupplierProvider } from "./api";
import { channelConfigurationErrors, channelSchemaVersion, type ChannelInstance } from "./channel-configuration";

export function supplierConfigurationInstance(account: SupplierAccount): ChannelInstance {
  return {
    id: String(account.id), category: "provision", implementation_id: account.provider,
    display_name: account.name, enabled: account.active, sort_order: 0,
    config: account.config ?? {}, secret_configured: account.credentials_configured,
    secure_fields: account.secure_fields, schema_version: account.schema_version ?? 1,
    version: account.version, validation_errors: account.configuration_errors,
  };
}

export function supplierConfigurationIssue(provider?: SupplierProvider, account?: SupplierAccount) {
  if (!provider) return "实现未编入当前构建";
  if (!provider.descriptor) return "配置字段定义不可用，请刷新供应商列表";
  if (provider.descriptor.factory_available === false || account?.factory_available === false) return "供应商运行工厂不可用";
  if (account?.configuration_status === "implementation_missing") return "实现未编入当前构建";
  if (account && (account.configuration_status === "schema_incompatible" || (account.schema_version ?? 1) !== channelSchemaVersion(provider.descriptor))) return "配置版本不兼容，请迁移配置或新建供应商";
  return "";
}

export function supplierAccountIssue(account: SupplierAccount, provider?: SupplierProvider, capability?: keyof SupplierProvider["capabilities"]) {
  const issue = supplierConfigurationIssue(provider, account);
  if (issue) return issue;
  if (!account.active) return "供应商已停用";
  if (capability && !provider?.capabilities[capability]) return capability === "catalog" ? "供应商不支持读取商品目录" : "供应商不支持读取商品详情";
  if (account.configuration_status === "invalid") return "供应商配置待修正";
  if (account.config === undefined) return "配置状态待确认，请刷新供应商列表";
  return channelConfigurationErrors(provider!.descriptor!, account.config, true, {}, [], supplierConfigurationInstance(account))[0] ?? "";
}
