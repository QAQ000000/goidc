"use client";

import { useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";

type ListState = { query: string; status: string; page: number };

function readState(params: URLSearchParams, states: readonly string[], defaultStatus: string): ListState {
  const requestedStatus = params.get("status");
  const pageText = params.get("page") ?? "0";
  const page = /^\d{1,6}$/.test(pageText) ? Number(pageText) : 0;
  return {
    query: params.get("q") ?? "",
    status: requestedStatus && states.includes(requestedStatus) ? requestedStatus : defaultStatus,
    page,
  };
}

function serializeState(state: ListState, defaultStatus: string) {
  const params = new URLSearchParams();
  if (state.query) params.set("q", state.query);
  if (state.status !== defaultStatus) params.set("status", state.status);
  if (state.page > 0) params.set("page", String(state.page));
  const query = params.toString();
  return query ? `?${query}` : "";
}

export function useAdminListNavigation(enabled: boolean, states: readonly string[], defaultStatus = "all", extraDefaults?: Readonly<Record<string, string>>) {
  const [extra, setExtra] = useState<Record<string, string>>({ ...extraDefaults });
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState("");
  const [status, setStatus] = useState(defaultStatus);
  const [page, setPage] = useState(0);
  const [ready, setReady] = useState(!enabled);

  useEffect(() => {
    if (!enabled) return;
    const apply = () => {
      const state = readState(new URLSearchParams(window.location.search), states, defaultStatus);
      if (extraDefaults) {
        const params = new URLSearchParams(window.location.search);
        setExtra(Object.fromEntries(Object.entries(extraDefaults).map(([key, value]) => [key, params.get(key) ?? value])));
      }
      setQuery(state.query);
      setDraft(state.query);
      setStatus(state.status);
      setPage(state.page);
      setReady(true);
    };
    apply();
    window.addEventListener("popstate", apply);
    return () => window.removeEventListener("popstate", apply);
  }, [enabled, states, defaultStatus, extraDefaults]);

  const params = new URLSearchParams(serializeState({ query, status, page }, defaultStatus));
  for (const [key, value] of Object.entries(extra)) if (value) params.set(key, value);
  const suffix = params.size ? `?${params.toString()}` : "";
  useEffect(() => {
    if (enabled && ready) window.history.replaceState(null, "", `${window.location.pathname}${suffix}`);
  }, [enabled, ready, suffix]);

  return { extra, setExtra, query, setQuery, draft, setDraft, status, setStatus, page, setPage, ready, suffix: enabled ? suffix : "" };
}

export function useAdminListReturnHref(path: string, states: readonly string[], defaultStatus = "all", extraDefaults?: Readonly<Record<string, string>>) {
  const params = useSearchParams();
  const result = new URLSearchParams(serializeState(readState(new URLSearchParams(params.toString()), states, defaultStatus), defaultStatus));
  for (const [key, fallback] of Object.entries(extraDefaults ?? {})) {
    const value = params.get(key) ?? fallback;
    if (value) result.set(key, value);
  }
  return path + (result.size ? `?${result}` : "");
}
