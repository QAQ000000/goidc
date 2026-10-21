// Adapted from Next Shadcn Dashboard Starter v1.0.0 (MIT).
// useSyncExternalStore keeps media queries safe during hydration and React 19 effects.
// See web/licenses/next-shadcn-dashboard-starter.MIT.
'use client';
import { useSyncExternalStore } from 'react';
const query = '(max-width: 767px)';
function subscribe(change: () => void) {
  const media = window.matchMedia(query);
  media.addEventListener('change', change);
  return () => media.removeEventListener('change', change);
}
function snapshot() { return window.matchMedia(query).matches; }
function serverSnapshot() { return false; }
export function useIsMobile() { return useSyncExternalStore(subscribe, snapshot, serverSnapshot); }
