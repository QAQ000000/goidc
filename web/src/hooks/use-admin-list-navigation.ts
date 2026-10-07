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

export function useAdminListNavigation(enabled: boolean, states: readonly string[], defaultStatus = "all") {
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState("");
  const [status, setStatus] = useState(defaultStatus);
  const [page, setPage] = useState(0);
  const [ready, setReady] = useState(!enabled);

  useEffect(() => {
    if (!enabled) return;
    const apply = () => {
      const state = readState(new URLSearchParams(window.location.search), states, defaultStatus);
      setQuery(state.query);
      setDraft(state.query);
      setStatus(state.status);
      setPage(state.page);
      setReady(true);
    };
    apply();
    window.addEventListener("popstate", apply);
    return () => window.removeEventListener("popstate", apply);
  }, [enabled, states, defaultStatus]);

  const suffix = serializeState({ query, status, page }, defaultStatus);
  useEffect(() => {
    if (enabled && ready) window.history.replaceState(null, "", `${window.location.pathname}${suffix}`);
  }, [enabled, ready, suffix]);

  return { query, setQuery, draft, setDraft, status, setStatus, page, setPage, ready, suffix: enabled ? suffix : "" };
}

export function useAdminListReturnHref(path: string, states: readonly string[], defaultStatus = "all") {
  const params = useSearchParams();
  return path + serializeState(readState(new URLSearchParams(params.toString()), states, defaultStatus), defaultStatus);
}
