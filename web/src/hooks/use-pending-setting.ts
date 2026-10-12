"use client";
import { useEffect, useRef, useState } from "react";

export type PendingSetting = { body: string; key: string; path?: string };

export function usePendingSetting(scope: string, actorId: number | null) {
  const pending = useRef<PendingSetting | null>(null);
  const [restored, setRestored] = useState<{ actorId: number | null; unknown: boolean }>({ actorId: null, unknown: false });
  const storageKey = actorId === null ? null : `zjmfgo:pending-setting:${actorId}:${scope}`;
  const ready = actorId !== null && restored.actorId === actorId;
  const unknown = ready && restored.unknown;

  useEffect(() => {
    pending.current = null;
    let found = false;
    if (storageKey) {
      try {
        const saved = sessionStorage.getItem(storageKey);
        if (saved) {
          const parsed: unknown = JSON.parse(saved);
          if (parsed && typeof parsed === "object" && "body" in parsed && "key" in parsed && typeof parsed.body === "string" && typeof parsed.key === "string") {
            pending.current = { body: parsed.body, key: parsed.key, path: "path" in parsed && typeof parsed.path === "string" ? parsed.path : undefined };
            found = true;
          }
        }
      } catch { /* Storage can be disabled; the in-memory retry still works. */ }
    }
    let active = true;
    queueMicrotask(() => { if (active) setRestored({ actorId, unknown: found }); });
    return () => { active = false; };
  }, [actorId, storageKey]);

  function setUnknown(value: boolean) {
    setRestored({ actorId, unknown: value });
  }

  function remember(value: PendingSetting) {
    pending.current = value;
    if (storageKey) {
      try { sessionStorage.setItem(storageKey, JSON.stringify(value)); } catch { /* See above. */ }
    }
  }

  function resolve() {
    pending.current = null;
    setUnknown(false);
    if (storageKey) {
      try { sessionStorage.removeItem(storageKey); } catch { /* See above. */ }
    }
  }

  return { pending, unknown, setUnknown, ready, remember, resolve };
}
