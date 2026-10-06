"use client";
import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
export function useResource<T>(path: string | null, keepPreviousData = true) {
  const [state, setState] = useState<{ path: string | null; data: T | null; error: Error | null }>({
    path: null,
    data: null,
    error: null,
  });
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    if (!path) return;
    const controller = new AbortController();
    api<T>(path, { signal: controller.signal })
      .then((data) => {
        if (!controller.signal.aborted) setState({ path, data, error: null });
      })
      .catch((error) => {
        if (!controller.signal.aborted)
          setState((current) => ({ path, data: keepPreviousData ? current.data : null, error }));
      });
    return () => controller.abort();
  }, [path, revision, keepPreviousData]);
  const refresh = useCallback(() => setRevision((v) => v + 1), []);
  const visible = path && (keepPreviousData || state.path === path);
  return { data: visible ? state.data : null, error: visible ? state.error : null, refresh };
}
