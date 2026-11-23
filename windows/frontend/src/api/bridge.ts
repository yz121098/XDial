import { useEffect, useState } from "react";
import type { AppSnapshot } from "../types";

export interface CloudHostProgressEvent {
  step: string;
  status: string;
  message?: string;
}

declare global {
  interface Window {
    __xdialPush?: (snap: AppSnapshot) => void;
    __xdialCloudHostEvent?: (ev: CloudHostProgressEvent) => void;
    __xdialNavigateSettings?: (section: string) => void;
  }
}

export async function rpc<T = unknown>(
  method: string,
  params?: unknown
): Promise<T> {
  const res = await fetch("/api/rpc", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ method, params: params ?? {} }),
  });
  const payload = (await res.json()) as {
    ok: boolean;
    data?: T;
    error?: string;
  };
  if (!payload.ok) {
    throw new Error(payload.error || "操作失败");
  }
  return payload.data as T;
}

type Listener = (snap: AppSnapshot) => void;
const listeners = new Set<Listener>();
let latestSnapshot: AppSnapshot | null = null;

function notifyAll(snap: AppSnapshot) {
  latestSnapshot = snap;
  for (const fn of listeners) {
    fn(snap);
  }
}

if (typeof window !== "undefined") {
  window.__xdialPush = (snap: AppSnapshot) => {
    notifyAll(snap);
  };
}

export async function refreshSnapshot(): Promise<AppSnapshot | null> {
  try {
    const res = await fetch("/api/snapshot", { cache: "no-store" });
    if (!res.ok) return null;
    const snap = (await res.json()) as AppSnapshot;
    notifyAll(snap);
    return snap;
  } catch {
    return null;
  }
}

export function useSnapshot(): AppSnapshot | null {
  const [snap, setSnap] = useState<AppSnapshot | null>(latestSnapshot);

  useEffect(() => {
    listeners.add(setSnap);
    if (!latestSnapshot) {
      void refreshSnapshot();
    }
    const onFocus = () => {
      void refreshSnapshot();
    };
    const onVis = () => {
      if (document.visibilityState === "visible") {
        void refreshSnapshot();
      }
    };
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onVis);
    // Periodic safety sync across Win32 ShowWindow/Hide transitions
    const pollId = window.setInterval(() => {
      void refreshSnapshot();
    }, 1500);
    return () => {
      listeners.delete(setSnap);
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onVis);
      window.clearInterval(pollId);
    };
  }, []);

  return snap;
}
