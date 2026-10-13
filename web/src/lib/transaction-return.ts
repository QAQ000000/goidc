export function transactionReturnHref(value: string | null) {
  if (!value || !/^\/admin\/transactions(?:\?|$)/.test(value) || /[\r\n\\]/.test(value)) return null;
  return value;
}
