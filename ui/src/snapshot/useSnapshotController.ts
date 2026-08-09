import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import type { ActiveElementIdentity, PopoverView, RefreshReason, ViewportState } from "../types/app";
import type { Snapshot } from "../types/snapshot";

const DEFAULT_REFRESH_INTERVAL_MS = 300_000;
const REFRESH_POLL_DELAYS_MS = [500, 1_000, 2_000, 4_000, 8_000] as const;
const READER_CONTEXT_TTL_MS = 20_000;
const READER_REFRESH_FLOOR_MS = 60_000;
const REFRESH_INTERVALS_MS = [30_000, 60_000, 120_000, 300_000, 0] as const;
const REFRESH_INTERVAL_STORAGE_KEY = "agentload.refreshIntervalMs.v5";

type SurfaceView = "popover" | "dashboard";

export function useSnapshotController({
  view,
  popoverView,
  shellRef,
}: {
  view: SurfaceView;
  popoverView: PopoverView;
  shellRef: RefObject<HTMLElement | null>;
}) {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [refreshInterval, setRefreshInterval] = useState<number>(() => initialRefreshInterval());
  const [surfaceVersion, setSurfaceVersion] = useState(0);
  const lastRenderTokenRef = useRef("");
  const lastSnapshotETagRef = useRef("");
  const lastSnapshotReceivedAtRef = useRef(0);
  const snapshotRef = useRef<Snapshot | null>(null);
  const popoverVisibleRef = useRef(true);
  const fetchInFlightRef = useRef<Promise<void> | null>(null);
  const refreshTimerRef = useRef<number | null>(null);
  const readerActiveUntilRef = useRef(0);

  const isSurfaceVisible = useCallback(
    () => surfaceVisible(view, popoverVisibleRef.current),
    [view],
  );
  const markReaderInteraction = useCallback(() => {
    readerActiveUntilRef.current = Date.now() + READER_CONTEXT_TTL_MS;
  }, []);
  const fetchSnapshot = useCallback(async (reason: RefreshReason = "auto") => {
    if (reason === "auto" && !surfaceVisible(view, popoverVisibleRef.current)) return;
    if (fetchInFlightRef.current) {
      if (reason === "auto") return;
      await fetchInFlightRef.current.catch(() => undefined);
    }
    const fetchWork = (async () => {
      const headers: HeadersInit = {};
      if (reason === "auto" && lastSnapshotETagRef.current) {
        headers["If-None-Match"] = lastSnapshotETagRef.current;
      }
      const response = await fetch("/api/snapshot", { cache: "no-store", headers });
      if (response.status === 304) {
        lastSnapshotReceivedAtRef.current = Date.now();
        setError(null);
        return;
      }
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const next = (await response.json()) as Snapshot;
      lastSnapshotETagRef.current = response.headers.get("ETag") || "";
      lastSnapshotReceivedAtRef.current = Date.now();
      const token = next.refresh_slot_id || next.generated_at || "";
      if (token && token === lastRenderTokenRef.current) {
        snapshotRef.current = next;
        setError(null);
        return;
      }
      const viewport = reason === "auto" && snapshotRef.current ? captureViewportState(shellRef.current) : null;
      lastRenderTokenRef.current = token;
      snapshotRef.current = next;
      setSnapshot(next);
      setError(null);
      if (viewport) restoreViewportState(viewport);
    })();
    fetchInFlightRef.current = fetchWork;
    try {
      await fetchWork;
    } finally {
      if (fetchInFlightRef.current === fetchWork) fetchInFlightRef.current = null;
    }
  }, [shellRef, view]);
  const requestRefreshSlot = useCallback(async () => {
    const params = new URLSearchParams();
    if (refreshInterval > 0) params.set("interval_ms", String(refreshInterval));
    const suffix = params.size ? `?${params.toString()}` : "";
    const response = await fetch(`/api/refresh${suffix}`, { method: "POST", headers: { "Content-Type": "application/json" } });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const body = (await response.json().catch(() => null)) as { refresh_slot_id?: string } | null;
    return typeof body?.refresh_slot_id === "string" ? body.refresh_slot_id : "";
  }, [refreshInterval]);
  const awaitRefreshedSnapshot = useCallback(async (reason: RefreshReason, targetSlot: string) => {
    const previousToken = lastRenderTokenRef.current;
    const hasFreshSlot = () => (targetSlot
      ? lastRenderTokenRef.current === targetSlot
      : Boolean(lastRenderTokenRef.current) && lastRenderTokenRef.current !== previousToken);
    if (hasFreshSlot()) return;
    for (const wait of REFRESH_POLL_DELAYS_MS) {
      await delay(wait);
      if (reason === "auto" && !surfaceVisible(view, popoverVisibleRef.current)) return;
      if (reason === "auto") await fetchSnapshot(reason).catch(() => undefined);
      else await fetchSnapshot(reason);
      if (hasFreshSlot()) return;
    }
  }, [fetchSnapshot, view]);
  const refreshSnapshot = useCallback(async () => {
    setRefreshing(true);
    try {
      const targetSlot = await requestRefreshSlot();
      await awaitRefreshedSnapshot("manual", targetSlot);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRefreshing(false);
    }
  }, [awaitRefreshedSnapshot, requestRefreshSlot]);
  const refreshAutomatically = useCallback(async () => {
    const targetSlot = await requestRefreshSlot().catch(() => null);
    if (targetSlot === null) return;
    await awaitRefreshedSnapshot("auto", targetSlot);
  }, [awaitRefreshedSnapshot, requestRefreshSlot]);
  const cycleRefreshInterval = useCallback(() => {
    const index = REFRESH_INTERVALS_MS.indexOf(refreshInterval as (typeof REFRESH_INTERVALS_MS)[number]);
    const next = REFRESH_INTERVALS_MS[(index + 1) % REFRESH_INTERVALS_MS.length];
    window.localStorage.setItem(REFRESH_INTERVAL_STORAGE_KEY, String(next));
    setRefreshInterval(next);
  }, [refreshInterval]);

  useEffect(() => {
    void fetchSnapshot("initial").catch((err) => setError(err instanceof Error ? err.message : String(err)));
  }, [fetchSnapshot]);

  useEffect(() => {
    let cancelled = false;
    const clearTimer = () => {
      if (refreshTimerRef.current !== null) {
        window.clearTimeout(refreshTimerRef.current);
        refreshTimerRef.current = null;
      }
    };
    const schedule = () => {
      clearTimer();
      const wait = effectiveAutoRefreshDelay(view, popoverView, refreshInterval, popoverVisibleRef.current, shellRef.current, readerActiveUntilRef.current);
      if (!wait || cancelled) return;
      const armedAt = Date.now();
      const fire = async () => {
        refreshTimerRef.current = null;
        if (cancelled || !surfaceVisible(view, popoverVisibleRef.current)) return;
        const required = effectiveAutoRefreshDelay(view, popoverView, refreshInterval, popoverVisibleRef.current, shellRef.current, readerActiveUntilRef.current);
        if (!required) return;
        const readyAt = Math.max(armedAt, readerActiveUntilRef.current - READER_CONTEXT_TTL_MS) + required;
        const remaining = readyAt - Date.now();
        if (remaining > 0) {
          refreshTimerRef.current = window.setTimeout(fire, remaining);
          return;
        }
        if (!fetchInFlightRef.current) await refreshAutomatically();
        if (!cancelled) schedule();
      };
      refreshTimerRef.current = window.setTimeout(fire, wait);
    };
    schedule();
    return () => {
      cancelled = true;
      clearTimer();
    };
  }, [fetchSnapshot, popoverView, refreshAutomatically, refreshInterval, shellRef, surfaceVersion, view]);

  useEffect(() => {
    const refreshIfStale = () => {
      if (!refreshInterval || !surfaceVisible(view, popoverVisibleRef.current)) return;
      const staleAfter = Math.max(DEFAULT_REFRESH_INTERVAL_MS, refreshInterval);
      if (!snapshotRef.current || Date.now() - lastSnapshotReceivedAtRef.current >= staleAfter) {
        void refreshAutomatically();
      }
    };
    const onVisibilityChange = () => {
      setSurfaceVersion((value) => value + 1);
      refreshIfStale();
    };
    const onPopoverShown = () => {
      popoverVisibleRef.current = true;
      setSurfaceVersion((value) => value + 1);
      refreshIfStale();
    };
    const onPopoverHidden = () => {
      popoverVisibleRef.current = false;
      setSurfaceVersion((value) => value + 1);
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    window.addEventListener("agentLoadPopoverShown", onPopoverShown);
    window.addEventListener("agentLoadPopoverHidden", onPopoverHidden);
    return () => {
      document.removeEventListener("visibilitychange", onVisibilityChange);
      window.removeEventListener("agentLoadPopoverShown", onPopoverShown);
      window.removeEventListener("agentLoadPopoverHidden", onPopoverHidden);
    };
  }, [refreshAutomatically, refreshInterval, view]);

  useEffect(() => {
    const shouldMarkReaderEvent = (event: Event) => {
      const target = event.target;
      if (event.type === "scroll" && (target === document || target === document.scrollingElement || target === document.documentElement)) return true;
      return target instanceof Node && Boolean(shellRef.current?.contains(target));
    };
    const onReaderEvent = (event: Event) => {
      if (shouldMarkReaderEvent(event)) markReaderInteraction();
    };
    document.addEventListener("click", onReaderEvent, true);
    document.addEventListener("keydown", onReaderEvent, true);
    document.addEventListener("scroll", onReaderEvent, true);
    return () => {
      document.removeEventListener("click", onReaderEvent, true);
      document.removeEventListener("keydown", onReaderEvent, true);
      document.removeEventListener("scroll", onReaderEvent, true);
    };
  }, [markReaderInteraction, shellRef]);

  return {
    snapshot,
    error,
    refreshing,
    refreshInterval,
    refreshSnapshot,
    cycleRefreshInterval,
    isSurfaceVisible,
    surfaceVisible: isSurfaceVisible(),
  };
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

function initialRefreshInterval(): number {
  const stored = window.localStorage.getItem(REFRESH_INTERVAL_STORAGE_KEY);
  if (stored === null) return DEFAULT_REFRESH_INTERVAL_MS;
  const raw = Number(stored);
  return REFRESH_INTERVALS_MS.includes(raw as (typeof REFRESH_INTERVALS_MS)[number]) ? raw : DEFAULT_REFRESH_INTERVAL_MS;
}

function surfaceVisible(view: SurfaceView, popoverVisible: boolean): boolean {
  if (document.visibilityState && document.visibilityState !== "visible") return false;
  return view !== "popover" || popoverVisible;
}

function effectiveAutoRefreshDelay(
  view: SurfaceView,
  popoverView: PopoverView,
  refreshInterval: number,
  popoverVisible: boolean,
  shell: HTMLElement | null,
  readerActiveUntil: number,
): number {
  if (!refreshInterval || !surfaceVisible(view, popoverVisible)) return 0;
  return readerContextActive(view, popoverView, shell, readerActiveUntil) ? Math.max(refreshInterval, READER_REFRESH_FLOOR_MS) : refreshInterval;
}

function readerContextActive(view: SurfaceView, popoverView: PopoverView, shell: HTMLElement | null, readerActiveUntil: number): boolean {
  if (readerActiveUntil > Date.now()) return true;
  if (document.scrollingElement && document.scrollingElement.scrollTop > 8) return true;
  const root = shell ?? document;
  if (root.querySelector('[aria-expanded="true"]')) return true;
  if (view === "popover") {
    if (popoverView === "trend" || popoverView === "diagnostics") return true;
    const scroller = root.querySelector<HTMLElement>(".popover-current-scroll");
    if (scroller && scroller.scrollTop > 8) return true;
  }
  return false;
}

function captureViewportState(shell: HTMLElement | null): ViewportState {
  const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const selectors = [".popover-current-scroll", ".popover-surface", ".dashboard-surface", ".project-tree-list", ".ledger-nav-list", ".log"];
  const scrollTargets = selectors.flatMap((selector) => {
    const root = shell ?? document;
    return Array.from(root.querySelectorAll<HTMLElement>(selector)).map((element, index) => ({
      selector,
      index,
      top: element.scrollTop,
      left: element.scrollLeft,
    }));
  });
  return {
    windowX: window.scrollX,
    windowY: window.scrollY,
    activeElement: active,
    activeIdentity: activeElementIdentity(active),
    scrollTargets,
  };
}

function restoreViewportState(state: ViewportState) {
  window.requestAnimationFrame(() => {
    state.scrollTargets.forEach((target) => {
      const element = document.querySelectorAll<HTMLElement>(target.selector)[target.index];
      if (!element) return;
      element.scrollTop = target.top;
      element.scrollLeft = target.left;
    });
    window.scrollTo(state.windowX, state.windowY);
    const active = state.activeElement?.isConnected ? state.activeElement : findElementFromIdentity(state.activeIdentity);
    if (active && document.activeElement !== active) active.focus({ preventScroll: true });
  });
}

function activeElementIdentity(active: HTMLElement | null): ActiveElementIdentity | null {
  if (!active) return null;
  const keyed = active.closest<HTMLElement>("[data-focus-key]");
  if (keyed?.dataset.focusKey) return { type: "focus-key", key: keyed.dataset.focusKey };
  if (active.id) return { type: "selector", selector: `#${cssEscape(active.id)}` };
  return null;
}

function findElementFromIdentity(identity: ActiveElementIdentity | null): HTMLElement | null {
  if (!identity) return null;
  if (identity.type === "selector") return document.querySelector<HTMLElement>(identity.selector);
  const nodes = Array.from(document.querySelectorAll<HTMLElement>("[data-focus-key]"));
  return nodes.find((node) => node.dataset.focusKey === identity.key) ?? null;
}

function cssEscape(value: string): string {
  if (window.CSS?.escape) return window.CSS.escape(value);
  return value.replace(/["\\#.;:[\],>+~*^$|=()\s]/g, "\\$&");
}
