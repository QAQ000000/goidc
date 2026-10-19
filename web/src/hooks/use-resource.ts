"use client";
import { useCallback, useEffect, useState } from "react";
import { api, authPortal, type AuthPortal } from "@/lib/api";
export function useResource<T>(path: string | null, keepPreviousData = true, requestedPortal?: AuthPortal) {
  const portal = authPortal(path ?? "", requestedPortal);
  const [state, setState] = useState<{ path: string | null; portal: AuthPortal; data: T | null; error: Error | null }>({
    path: null,
    portal,
    data: null,
    error: null,
  });
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    if (!path) return;
    const controller = new AbortController();
    api<T>(path, { signal: controller.signal, portal })
      .then((data) => {
        if (!controller.signal.aborted) setState({ path, portal, data, error: null });
      })
      .catch((error) => {
        if (!controller.signal.aborted)
          setState((current) => ({ path, portal, data: keepPreviousData && current.portal === portal ? current.data : null, error }));
      });
    return () => controller.abort();
  }, [path, portal, revision, keepPreviousData]);
  const refresh = useCallback(() => setRevision((v) => v + 1), []);
  const visible = path && state.portal === portal && (keepPreviousData || state.path === path);
  return { data: visible ? state.data : null, error: visible ? state.error : null, refresh };
}
