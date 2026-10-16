"use client";

import { useRef, useState } from "react";
import { CreditCard, ExternalLink } from "lucide-react";
import { api, ApiError, money } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "./ui/select";

type Provider = { name: string; label: string; instance_id: string; simulation: boolean };
export type PaymentRecord = {
  id: number;
  provider: string;
  provider_payment_id: string;
  external_transaction_id?: string;
  checkout_url?: string;
  status: string;
  expired: boolean;
  amount_cents: number;
  created_at: string;
  completed_at: string | null;
  check_status?: string | null;
  check_code?: string | null;
  checked_at?: string | null;
  check_source?: "manual" | "worker" | null;
  query_enabled?: boolean;
  next_reconcile_at?: string | null;
  auto_reconcile_attempts?: number;
  reconciliation_exhausted?: boolean;
};
export const paymentLabels: Record<string, string> = {
  mock: "模拟支付",
  epay_alipay: "支付宝",
  epay_wxpay: "微信支付",
  epay_qqpay: "QQ 钱包",
  epay_bank: "网银支付",
};

function checkoutLink(value: string) {
  try {
    const url = new URL(value);
    return !url.username &&
      !url.password &&
      (url.protocol === "https:" ||
        (url.protocol === "http:" &&
          ["localhost", "127.0.0.1", "[::1]"].includes(url.hostname)))
      ? value
      : "";
  } catch {
    return "";
  }
}

export function OnlinePayment({
  invoice,
  refresh,
}: {
  invoice: {
    id: number;
    version: number;
    amount_cents: number;
    import_review_required: boolean;
    payment_pending: boolean;
    payments?: PaymentRecord[];
    topup?: { status: string };
  };
  refresh: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [selectedOption, setSelectedOption] = useState("");
  const keys = useRef<Record<string, string>>({});
  const providers = useResource<Provider[]>(
    open ? "/payment-providers" : null,
    false,
  );
  const options = (providers.data ?? []).filter(value => !invoice.topup || !value.simulation);
  const chosen =
    options.find((value) => `${value.name}:${value.instance_id}` === selectedOption) ||
    options.find((value) => !value.simulation) ||
    options[0];
  const provider = chosen?.name || "";
  const instanceID = chosen?.instance_id || "";
  const [checkout, setCheckout] = useState("");
  const pending = invoice.payments?.find(
    (value) =>
      (value.status === "pending" || (invoice.topup?.status === "accepted" && value.status === "expired")) &&
      value.provider !== "mock" &&
      value.checkout_url,
  );
  const link = checkoutLink(checkout || pending?.checkout_url || "");
  async function create() {
    setBusy(true);
    setError("");
    keys.current[`${provider}:${instanceID}`] ??=
      `web-payment-${invoice.id}-${crypto.randomUUID()}`;
    try {
      const result = await api<{ checkout_url: string }>(
        `/invoices/${invoice.id}/payments`,
        {
          method: "POST",
          body: JSON.stringify({ provider, channel_instance_id: instanceID, expected_version: invoice.version }),
          headers: { "Idempotency-Key": keys.current[`${provider}:${instanceID}`] },
        },
      );
      setCheckout(result.checkout_url);
      setOpen(false);
      refresh();
    } catch (e) {
      setError((e as Error).message);
      if (e instanceof ApiError && e.status === 409) refresh();
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <Button
        size="sm"
        variant="outline"
        disabled={
          busy || invoice.import_review_required || invoice.payment_pending || (!!invoice.topup && invoice.topup.status !== "created")
        }
        onClick={() => setOpen(true)}
      >
        <CreditCard />
        在线支付
      </Button>
      {link && (
        <Button variant="outline" size="sm" asChild>
          <a href={link} target="_blank" rel="noopener noreferrer">
            <ExternalLink />
            {invoice.topup?.status === "accepted" ? "查看原支付单" : "前往付款"}
          </a>
        </Button>
      )}
      {invoice.topup?.status === "accepted" && (pending?.expired || pending?.status === "expired") && <p className="text-xs text-muted-foreground whitespace-normal">本地已过期，收款结果仍待核实，请勿重复创建付款。</p>}
      <Dialog
        open={open}
        onOpenChange={(value) => {
          if (!busy) setOpen(value);
        }}
      >
        <DialogContent className="min-w-0 w-[calc(100vw-2rem)] max-w-lg grid-cols-[minmax(0,1fr)] overflow-x-hidden max-h-[calc(100dvh-2rem)] overflow-y-auto p-4 sm:p-6">
          <DialogHeader>
            <DialogTitle>在线支付</DialogTitle>
          </DialogHeader>
          <p className="money text-xl font-semibold">
            {money(invoice.amount_cents)}
          </p>
          <label className="grid min-w-0 gap-2 text-sm">
            支付渠道
            <Select
              value={chosen ? `${chosen.name}:${chosen.instance_id}` : ""}
              disabled={busy}
              onValueChange={setSelectedOption}
            >
              <SelectTrigger aria-label="支付渠道" className="w-full min-w-0 max-w-full [&>span]:min-w-0 [&>span]:truncate">
                <SelectValue placeholder="选择支付渠道" />
              </SelectTrigger>
              <SelectContent className="max-w-[calc(100vw-3rem)]">
                {options.map((value) => (
                  <SelectItem key={`${value.name}:${value.instance_id}`} value={`${value.name}:${value.instance_id}`} className="min-w-0 [&_span]:truncate">
                    {value.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </label>
          {!providers.error && providers.data && !options.length && <p className="text-sm text-muted-foreground">暂无可用在线支付渠道，请稍后重试。</p>}
          {(error || providers.error) && (
            <p role="alert" className="text-sm text-destructive break-words">
              {error || providers.error?.message}
            </p>
          )}
          <DialogFooter className="min-w-0 [&>button]:max-w-full">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setOpen(false)}
            >
              返回
            </Button>
            <Button
              disabled={busy || !!providers.error || !provider || !options.length}
              onClick={() => void create()}
            >
              <CreditCard />
              {busy ? "处理中" : "创建支付单"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
