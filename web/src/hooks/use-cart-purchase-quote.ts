"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { PurchasePricingLine } from "@/components/purchase-discount-details";

export type CartPurchaseQuote = {
  quote_hash: string;
  subtotal_cents: number;
  group_discount_cents: number;
  promo_discount_cents: number;
  discount_cents: number;
  total_cents: number;
  items: PurchasePricingLine[];
  promo_code?: string;
};

// The request body identifies the exact version and selection. A pending or
// obsolete response cannot authorize checkout for a different selection.
export function useCartPurchaseQuote(requestBody: string | null) {
  const [attempt, setAttempt] = useState(0);
  const [result, setResult] = useState<{ key: string; quote?: CartPurchaseQuote; error?: string } | null>(null);
  const key = requestBody === null ? null : `${requestBody}:${attempt}`;
  useEffect(() => {
    if (requestBody === null || key === null) return;
    const controller = new AbortController();
    let current = true;
    void api<CartPurchaseQuote>("/cart/quote", { method: "POST", body: requestBody, signal: controller.signal })
      .then(quote => {
        if (!quote.quote_hash) throw new Error("报价缺少确认凭证，请重新报价");
        if (current) setResult({ key, quote });
      })
      .catch(error => { if (current) setResult({ key, error: error instanceof Error ? error.message : "报价失败，请重试" }); });
    return () => { current = false; controller.abort(); };
  }, [requestBody, key]);
  const matching = key !== null && result?.key === key;
  return {
    quote: matching ? result.quote : undefined,
    error: matching ? result.error : undefined,
    loading: key !== null && !matching,
    retry: () => setAttempt(value => value + 1),
  };
}
