"use client";
import { useCallback, useEffect, useMemo, useState } from "react";
import { api, authPortal, type AuthPortal } from "@/lib/api";
export function useResource<T>(path: string | null, keepPreviousData = true, requestedPortal?: AuthPortal) {
  const portal = authPortal(path ?? "", requestedPortal);
  const [revision, setRevision] = useState(0);
  const request = useMemo(() => ({ path, portal, revision, keepPreviousData }), [path, portal, revision, keepPreviousData]);
  const [state, setState] = useState<{ path: string | null; portal: AuthPortal; request: typeof request | null; data: T | null; error: Error | null; updatedAt: number | null }>({
    path: null,
    portal,
    request: null,
    data: null,
    error: null,
    updatedAt: null,
  });
  useEffect(() => {
    const { path, portal, keepPreviousData } = request;
    if (!path) return;
    const controller = new AbortController();
    api<T>(path, { signal: controller.signal, portal })
      .then((data) => {
        if (!controller.signal.aborted) setState({ path, portal, request, data, error: null, updatedAt: Date.now() });
      })
      .catch((error) => {
        if (!controller.signal.aborted)
          setState((current) => ({ path, portal, request, data: keepPreviousData && current.portal === portal ? current.data : null, error, updatedAt: null }));
      });
    return () => controller.abort();
  }, [request]);
  const refresh = useCallback(() => setRevision((v) => v + 1), []);
  const visible = path && state.portal === portal && (keepPreviousData || state.path === path);
  const isLoading = !!path && state.request !== request;
  return { data: visible ? state.data : null, error: visible ? state.error : null, isLoading, updatedAt: visible ? state.updatedAt : null, refresh };
}
