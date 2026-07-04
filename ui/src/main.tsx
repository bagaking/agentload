import React, { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { Activity, ArrowUpRight, Bot, ChevronDown, Copy, ExternalLink, Gauge, GitBranch, Info, Languages, Layers, Moon, RefreshCw, Search, Server, Sun, Terminal, X } from "lucide-react";
import { copy, type Lang } from "./i18n";
import { agentRoleLabel, buildToolSessionGroups, confidenceLabel, freshnessLabel, hiddenToolSessionCount, mappingMethodLabel, normalizedRole, orderedProjects, projectEvidenceItems, projectRoleCounts, roleHintLabel, roleLabel, sessionEvidenceItems, sessionIDsText, sessionIdentity, sessionsForProject, threadSourceLabel, toolBadgeLabel, toolDisplayName, toolIconName } from "./lib/activityModel";
import { activeWindowLabel, buildRailItems, coordinationPostureLabel, currentMeaningLead, currentMeaningPoints, currentPeerScale, dashboardProjectLead, dashboardProjectMeta, deferredScanValue, mappingHealthText, metricState, primaryEvidenceNote, renderLogText, resolveSelection, statusTone, transcriptScanNote, transcriptScanSummary } from "./lib/dashboardModel";
import { clampPct, countLabel, formatAge, formatCPU, formatCopy, formatDateTime, formatMemory, formatPct, formatRefreshInterval, formatTokenCount, formatTokenUsageSummary, pctPart, safeID, shortID, tokenUsageHasValue } from "./lib/format";
import { TrendSuite } from "./trend/TrendSuite";
import { TREND_RANGES, type TrendLane, type TrendRange } from "./trend/types";
import type { ActiveElementIdentity, LogTab, PopoverView, ProjectMetricObject, ProjectMetricScope, RailItem, RailTab, RefreshReason, RoleCounts, SelectedView, Selection, Theme, ViewportState } from "./types/app";
import type { AgeBucketSnapshot, HostApp, LiveProcess, LiveSession, ProjectSnapshot, ProjectTool, Snapshot, TokenUsage } from "./types/snapshot";
import "./styles.css";

const BRAND_NAME = "Agent Load";
const ACTIVE = new Set(["active", "running", "queued"]);
const DEFAULT_REFRESH_INTERVAL_MS = 300_000;
const MANUAL_REFRESH_SETTLE_MS = 450;
const AUTO_REFRESH_SETTLE_MS = 650;
const READER_CONTEXT_TTL_MS = 20_000;
const READER_REFRESH_FLOOR_MS = 60_000;
const REFRESH_INTERVALS_MS = [30_000, 60_000, 120_000, 300_000, 0] as const;
const REFRESH_INTERVAL_STORAGE_KEY = "agentload.refreshIntervalMs.v5";
const INSPECTOR_INITIAL_LIMIT = 12;
const PROCESS_LEDGER_INITIAL_LIMIT = 40;
type ProcessFilter =
  | { kind: "all"; id: "all" }
  | { kind: "runtime"; id: string }
  | { kind: "host"; id: string }
  | { kind: "role"; id: "direct" | "subagent" | "unknown" | "unmapped" };
type HoverDetailPayload =
  | { kind: "project"; id: string; title: string; detail: string; meta?: string }
  | { kind: "session"; id: string; title: string; metrics: Array<{ label: string; value: string }>; tokenParts: Array<{ label: string; value: string }>; meta: string };
type HoverDetailEvent = React.PointerEvent<HTMLElement> | React.FocusEvent<HTMLElement>;
type HoverDetailSink = (detail: HoverDetailPayload | null, event?: HoverDetailEvent) => void;
type HoverDetailState = { detail: HoverDetailPayload; x: number; y: number; visible: boolean };

declare global {
  interface Window {
    webkit?: {
      messageHandlers?: {
        agentLoadAction?: { postMessage: (body: unknown) => void };
        agentLoadResize?: { postMessage: (body: unknown) => void };
      };
    };
  }
}

function App() {
  const view = window.location.pathname === "/dashboard" ? "dashboard" : "popover";
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [lang, setLang] = useState<Lang>(() => initialLang());
  const [theme, setTheme] = useState<Theme>(() => initialTheme());
  const [railTab, setRailTab] = useState<RailTab>("projects");
  const [query, setQuery] = useState("");
  const [selection, setSelection] = useState<Selection>({ type: "overview", id: "overview" });
  const [logTab, setLogTab] = useState<LogTab>("summary");
  const [popoverView, setPopoverView] = useState<PopoverView>("online");
  const [trendRange, setTrendRange] = useState<TrendRange>("1D");
  const [trendSelection, setTrendSelection] = useState<Record<TrendLane, string | undefined>>({ history: undefined, runtime: undefined });
  const [refreshInterval, setRefreshInterval] = useState<number>(() => initialRefreshInterval());
  const shellRef = useRef<HTMLDivElement | null>(null);
  const lastRenderTokenRef = useRef("");
  const lastSnapshotETagRef = useRef("");
  const lastSnapshotReceivedAtRef = useRef(0);
  const snapshotRef = useRef<Snapshot | null>(null);
  const popoverVisibleRef = useRef(true);
  const fetchInFlightRef = useRef<Promise<void> | null>(null);
  const refreshTimerRef = useRef<number | null>(null);
  const readerActiveUntilRef = useRef(0);
  const readerScheduleBumpAfterRef = useRef(0);
  const [surfaceVersion, setSurfaceVersion] = useState(0);

  const t = useCallback((key: string) => copy[lang][key] || copy.en[key] || key, [lang]);
  const markReaderInteraction = useCallback(() => {
    const now = Date.now();
    readerActiveUntilRef.current = now + READER_CONTEXT_TTL_MS;
    if (now < readerScheduleBumpAfterRef.current) return;
    readerScheduleBumpAfterRef.current = now + 1000;
    setSurfaceVersion((value) => value + 1);
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
      const response = await fetch("/api/snapshot", {
        cache: "no-store",
        headers,
      });
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
      if (reason === "auto" && token && token === lastRenderTokenRef.current) {
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
  }, [view]);
  const requestRefreshSlot = useCallback(async () => {
    const params = new URLSearchParams();
    if (refreshInterval > 0) params.set("interval_ms", String(refreshInterval));
    const suffix = params.size ? `?${params.toString()}` : "";
    const response = await fetch(`/api/refresh${suffix}`, { method: "POST", headers: { "Content-Type": "application/json" } });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
  }, [refreshInterval]);
  const refreshSnapshot = useCallback(async () => {
    setRefreshing(true);
    try {
      await requestRefreshSlot();
      await delay(MANUAL_REFRESH_SETTLE_MS);
      await fetchSnapshot("manual");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRefreshing(false);
    }
  }, [fetchSnapshot, requestRefreshSlot]);
  const refreshAutomatically = useCallback(async () => {
    await requestRefreshSlot().catch(() => undefined);
    await delay(AUTO_REFRESH_SETTLE_MS);
    await fetchSnapshot("auto").catch(() => undefined);
  }, [fetchSnapshot, requestRefreshSlot]);

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
      const delay = effectiveAutoRefreshDelay(view, popoverView, refreshInterval, popoverVisibleRef.current, shellRef.current, readerActiveUntilRef.current);
      if (!delay || cancelled) return;
      refreshTimerRef.current = window.setTimeout(async () => {
        refreshTimerRef.current = null;
        if (cancelled) return;
        if (!surfaceVisible(view, popoverVisibleRef.current)) return;
        if (!fetchInFlightRef.current) {
          await refreshAutomatically();
        }
        if (!cancelled) schedule();
      }, delay);
    };
    schedule();
    return () => {
      cancelled = true;
      clearTimer();
    };
  }, [fetchSnapshot, popoverView, refreshAutomatically, refreshInterval, surfaceVersion, view]);
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
  }, [markReaderInteraction]);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    window.localStorage.setItem("agentload.theme", theme);
  }, [theme]);
  useEffect(() => {
    document.body.dataset.view = view;
  }, [view]);
  useEffect(() => {
    document.documentElement.lang = htmlLang(lang);
    window.localStorage.setItem("agentload.lang", lang);
  }, [lang]);
  useEffect(() => {
    if (view !== "popover") return;
    const target = shellRef.current;
    if (!target) return;
    let observer: ResizeObserver | null = null;
    let resizeFrame = 0;
    let settledTimer = 0;
    let lastHeight = 0;
    const postResize = () => {
      if (!surfaceVisible(view, popoverVisibleRef.current)) return;
      const height = Math.ceil(Math.min(Math.max(target.scrollHeight || 420, 320), 580));
      if (!height || Math.abs(height - lastHeight) < 2) return;
      lastHeight = height;
      window.webkit?.messageHandlers?.agentLoadResize?.postMessage({ height });
    };
    const requestResize = () => {
      if (!surfaceVisible(view, popoverVisibleRef.current) || resizeFrame) return;
      resizeFrame = window.requestAnimationFrame(() => {
        resizeFrame = 0;
        postResize();
      });
    };
    const requestSettledResize = () => {
      requestResize();
      window.clearTimeout(settledTimer);
      settledTimer = window.setTimeout(requestResize, 120);
    };
    const startResize = () => {
      if (!surfaceVisible(view, popoverVisibleRef.current)) return;
      if (!observer) {
        observer = new ResizeObserver(requestResize);
        observer.observe(target);
      }
      requestSettledResize();
    };
    const stopResize = () => {
      observer?.disconnect();
      observer = null;
      if (resizeFrame) window.cancelAnimationFrame(resizeFrame);
      resizeFrame = 0;
      window.clearTimeout(settledTimer);
      settledTimer = 0;
    };
    const onVisible = () => startResize();
    const onHidden = () => stopResize();
    const onDocumentVisibility = () => {
      if (surfaceVisible(view, popoverVisibleRef.current)) startResize();
      else stopResize();
    };
    startResize();
    window.addEventListener("agentLoadPopoverShown", onVisible);
    window.addEventListener("agentLoadPopoverHidden", onHidden);
    document.addEventListener("visibilitychange", onDocumentVisibility);
    return () => {
      stopResize();
      window.removeEventListener("agentLoadPopoverShown", onVisible);
      window.removeEventListener("agentLoadPopoverHidden", onHidden);
      document.removeEventListener("visibilitychange", onDocumentVisibility);
    };
  }, [error, logTab, popoverView, railTab, refreshInterval, selection, snapshot, trendRange, trendSelection, view]);

  const selected = useMemo(() => resolveSelection(t, snapshot, selection, BRAND_NAME), [t, snapshot, selection]);
  const compact = view === "popover";
  const running = refreshing;

  const cycleRefreshInterval = () => {
    const index = REFRESH_INTERVALS_MS.indexOf(refreshInterval as (typeof REFRESH_INTERVALS_MS)[number]);
    const next = REFRESH_INTERVALS_MS[(index + 1) % REFRESH_INTERVALS_MS.length];
    window.localStorage.setItem(REFRESH_INTERVAL_STORAGE_KEY, String(next));
    setRefreshInterval(next);
  };

  return (
    <div className={`app app-${view}`} ref={shellRef}>
      <Topbar
        t={t}
        lang={lang}
        setLang={setLang}
        theme={theme}
        setTheme={setTheme}
        compact={compact}
        running={running}
        error={error}
        refreshSnapshot={refreshSnapshot}
        refreshInterval={refreshInterval}
        cycleRefreshInterval={cycleRefreshInterval}
      />
      {view === "popover" ? (
        <>
          <PopoverSurface
            t={t}
            snapshot={snapshot}
            error={error}
            selection={selection}
            setSelection={setSelection}
            popoverView={popoverView}
            trendRange={trendRange}
            setTrendRange={setTrendRange}
            trendSelection={trendSelection}
            setTrendSelection={setTrendSelection}
          />
          <PopoverFooter
            t={t}
            snapshot={snapshot}
            popoverView={popoverView}
            setPopoverView={setPopoverView}
            refreshInterval={refreshInterval}
            cycleRefreshInterval={cycleRefreshInterval}
          />
        </>
      ) : (
        <DashboardSurface
          t={t}
          snapshot={snapshot}
          error={error}
          running={running}
          refreshSnapshot={refreshSnapshot}
          refreshInterval={refreshInterval}
          cycleRefreshInterval={cycleRefreshInterval}
          selection={selection}
          setSelection={setSelection}
          railTab={railTab}
          setRailTab={setRailTab}
          query={query}
          setQuery={setQuery}
          trendRange={trendRange}
          setTrendRange={setTrendRange}
          trendSelection={trendSelection}
          setTrendSelection={setTrendSelection}
        />
      )}
    </div>
  );
}

function PopoverSurface({
  t,
  snapshot,
  error,
  selection,
  setSelection,
  popoverView,
  trendRange,
  setTrendRange,
  trendSelection,
  setTrendSelection,
}: {
  t: (key: string) => string;
  snapshot: Snapshot | null;
  error: string | null;
  selection: Selection;
  setSelection: (value: Selection) => void;
  popoverView: PopoverView;
  trendRange: TrendRange;
  setTrendRange: (value: TrendRange) => void;
  trendSelection: Record<TrendLane, string | undefined>;
  setTrendSelection: React.Dispatch<React.SetStateAction<Record<TrendLane, string | undefined>>>;
}) {
  const [hoverDetail, setHoverDetailState] = useState<HoverDetailState | null>(null);
  const hideHoverDetailTimerRef = useRef<number | null>(null);
  const clearHoverDetailTimer = useCallback(() => {
    if (hideHoverDetailTimerRef.current !== null) {
      window.clearTimeout(hideHoverDetailTimerRef.current);
      hideHoverDetailTimerRef.current = null;
    }
  }, []);
  const setHoverDetail = useCallback<HoverDetailSink>((detail, event) => {
    clearHoverDetailTimer();
    if (!detail) {
      hideHoverDetailTimerRef.current = window.setTimeout(() => {
        setHoverDetailState((current) => current ? { ...current, visible: false } : null);
      }, 140);
      return;
    }
    const point = hoverPointFromEvent(event);
    setHoverDetailState({ detail, x: point.x, y: point.y, visible: true });
  }, [clearHoverDetailTimer]);
  useEffect(() => {
    clearHoverDetailTimer();
    setHoverDetailState(null);
  }, [clearHoverDetailTimer, popoverView, snapshot?.generated_at, snapshot?.refresh_slot_id]);
  useEffect(() => () => clearHoverDetailTimer(), [clearHoverDetailTimer]);

  if (!snapshot) return <EmptySurface t={t} compact error={error} />;
  return (
    <main className="popover-surface">
      <div className="popover-current-surface">
        <div className="popover-current-scroll">
          <ErrorBanner t={t} error={error} compact />
          <div className="popover-view-shell">
            <section
              className="popover-view-panel online"
              id="popover-panel-online"
              role="tabpanel"
              aria-labelledby="popover-view-online"
              hidden={popoverView !== "online"}
            >
              <PopoverAuditShell t={t} snapshot={snapshot} selection={selection} setSelection={setSelection} setHoverDetail={setHoverDetail} />
            </section>
            <section
              className="popover-view-panel trend"
              id="popover-panel-trend"
              role="tabpanel"
              aria-labelledby="popover-view-trend"
              hidden={popoverView !== "trend"}
            >
              <TrendSuite
                t={t}
                snapshot={snapshot}
                compact
                range={trendRange}
                setRange={setTrendRange}
                trendSelection={trendSelection}
                setTrendSelection={setTrendSelection}
              />
            </section>
          </div>
        </div>
        {popoverView === "online" ? <PopoverHoverInspector t={t} detail={hoverDetail} /> : null}
      </div>
    </main>
  );
}

function PopoverFooter({
  t,
  snapshot,
  popoverView,
  setPopoverView,
  refreshInterval,
  cycleRefreshInterval,
}: {
  t: (key: string) => string;
  snapshot: Snapshot | null;
  popoverView: PopoverView;
  setPopoverView: (value: PopoverView) => void;
  refreshInterval: number;
  cycleRefreshInterval: () => void;
}) {
  const generated = snapshot?.generated_at ? formatDateTime(snapshot.generated_at) : t("noData");
  const active = (snapshot?.current?.active_burst_concurrency ?? 0) > 0;
  const stateLabel = snapshot ? metricState(snapshot, t) : t("noData");
  return (
    <footer className={`popover-footer ${active ? "is-active" : ""}`}>
      <div className={`footer-meta ${active ? "is-active" : ""}`} role="status" title={stateLabel} aria-label={`${stateLabel} ${generated}`}>
        <span className={`state-dot footer-state-dot ${snapshot ? "observed" : "idle"} ${active ? "is-active" : ""}`} aria-hidden="true" />
        <span className="footer-time">{generated}</span>
        <button className={`refresh-interval footer-interval ${refreshInterval ? "" : "is-paused"}`} type="button" data-focus-key={focusKey("refresh-interval", "popover")} onClick={cycleRefreshInterval} title={t("autoRefresh")} aria-label={t("autoRefresh")}>
          <RefreshCw size={11} aria-hidden="true" />
          <span>{formatRefreshInterval(refreshInterval, t)}</span>
        </button>
      </div>
      <div className="popover-footer-controls">
        <div className="popover-view-switch" role="tablist" aria-label={t("view")}>
          {(["online", "trend"] as PopoverView[]).map((view) => (
            <button
              key={view}
              id={`popover-view-${view}`}
              className={popoverView === view ? "is-active" : ""}
              type="button"
              role="tab"
              aria-selected={popoverView === view}
              aria-controls={`popover-panel-${view}`}
              tabIndex={popoverView === view ? 0 : -1}
              data-focus-key={focusKey("popover-view", view)}
              onClick={() => setPopoverView(view)}
            >
              {view === "online" ? <Activity size={13} /> : <Gauge size={13} />}
              <span>{view === "online" ? t("online") : t("trend")}</span>
            </button>
          ))}
        </div>
        <button className="footer-link" type="button" data-focus-key={focusKey("open-dashboard", "popover")} onClick={() => postHostAction("open_dashboard")} title={t("dashboard")} aria-label={t("dashboard")}>
          <ArrowUpRight size={14} />
          <span>{t("dashboard")}</span>
        </button>
      </div>
    </footer>
  );
}

function DashboardSurface({
  t,
  snapshot,
  error,
  running,
  refreshSnapshot,
  refreshInterval,
  cycleRefreshInterval,
  selection,
  setSelection,
  railTab,
  setRailTab,
  query,
  setQuery,
  trendRange,
  setTrendRange,
  trendSelection,
  setTrendSelection,
}: {
  t: (key: string) => string;
  snapshot: Snapshot | null;
  error: string | null;
  running: boolean;
  refreshSnapshot: () => void;
  refreshInterval: number;
  cycleRefreshInterval: () => void;
  selection: Selection;
  setSelection: (value: Selection) => void;
  railTab: RailTab;
  setRailTab: (tab: RailTab) => void;
  query: string;
  setQuery: (value: string) => void;
  trendRange: TrendRange;
  setTrendRange: (value: TrendRange) => void;
  trendSelection: Record<TrendLane, string | undefined>;
  setTrendSelection: React.Dispatch<React.SetStateAction<Record<TrendLane, string | undefined>>>;
}) {
  if (!snapshot) return <EmptySurface t={t} error={error} />;
  const railItems = buildRailItems(t, snapshot, railTab, query);
  return (
    <main className="dashboard-surface">
      <ErrorBanner t={t} error={error} />
      <DashboardMasthead
        t={t}
        snapshot={snapshot}
        running={running}
        refreshSnapshot={refreshSnapshot}
        refreshInterval={refreshInterval}
        cycleRefreshInterval={cycleRefreshInterval}
      />
      <section className="dash-front-band">
        <section className="dash-field-index">
          <DashboardBandHead kicker={t("runtimeField")} title={t("activityCounts")} meta={dashboardProjectMeta(t, snapshot)} />
          <DashboardFieldGrid t={t} snapshot={snapshot} />
          <CurrentMeaningStrip t={t} snapshot={snapshot} compact />
        </section>
        <DashboardEvidenceColumn t={t} snapshot={snapshot} />
      </section>

      <section className="dash-atlas-band">
        <DashboardBandHead kicker={t("liveLedger")} title={t("projectSessionTree")} meta={`${snapshot.live_sessions?.length ?? 0} ${t("sessions")}`} />
        <div className="dash-atlas-grid">
          <div className="dash-atlas-panel">
            <ProjectAtlas t={t} snapshot={snapshot} selection={selection} setSelection={setSelection} limit={14} defaultExpandedCount={1} showHead={false} />
          </div>
          <DashboardSideRails t={t} snapshot={snapshot} />
        </div>
      </section>

      <TrendSuite
        t={t}
        snapshot={snapshot}
        range={trendRange}
        setRange={setTrendRange}
        trendSelection={trendSelection}
        setTrendSelection={setTrendSelection}
      />

      <section className="dash-ledger-band">
        <DashboardBandHead kicker={t("liveLedger")} title={t("processLedger")} meta={`${snapshot.live_processes?.length ?? 0} ${t("processes")}`} />
        <ProcessLedger t={t} snapshot={snapshot} selection={selection} setSelection={setSelection} />
      </section>

      <DashboardInspectorStrip
        t={t}
        activeTab={railTab}
        setActiveTab={setRailTab}
        query={query}
        setQuery={setQuery}
        items={railItems}
        selection={selection}
        setSelection={setSelection}
      />
    </main>
  );
}

function DashboardMasthead({
  t,
  snapshot,
  running,
  refreshSnapshot,
  refreshInterval,
  cycleRefreshInterval,
}: {
  t: (key: string) => string;
  snapshot: Snapshot;
  running: boolean;
  refreshSnapshot: () => void;
  refreshInterval: number;
  cycleRefreshInterval: () => void;
}) {
  const stats = snapshot.transcript_stats ?? {};
  const generated = snapshot.generated_at ? formatDateTime(snapshot.generated_at) : t("unavailable");
  const sourceState = stats.cached ? t("cached") : t("fresh");
  const subtitle = [sourceState, formatRefreshInterval(refreshInterval, t), generated].join(" · ");
  return (
    <section className="dashboard-masthead" aria-label={t("dashboard")}>
      <div className="dashboard-masthead-copy">
        <div className="dashboard-masthead-kicker">
          <span className={`field-status ${statusTone(snapshot)}`}>{metricState(snapshot, t)}</span>
          <span>{t("dashboard")}</span>
        </div>
        <h1>{BRAND_NAME}</h1>
        <p>{subtitle}</p>
      </div>
      <div className="dashboard-masthead-side">
        <div className="dashboard-masthead-actions">
          <button className="ghost-btn dashboard-refresh-action" type="button" data-focus-key={focusKey("refresh", "dashboard")} onClick={refreshSnapshot} disabled={running} aria-label={t("refresh")}>
            <RefreshCw size={14} className={running ? "spin" : ""} />
            <span>{running ? t("running") : t("refresh")}</span>
          </button>
          <button className={`refresh-interval dashboard-refresh-interval ${refreshInterval ? "" : "is-paused"}`} type="button" data-focus-key={focusKey("refresh-interval", "dashboard")} onClick={cycleRefreshInterval} title={t("autoRefresh")} aria-label={t("autoRefresh")}>
            <span>{formatRefreshInterval(refreshInterval, t)}</span>
          </button>
        </div>
        <div className="dashboard-masthead-meta">
          <span>
            <b>{t("observed")}</b>
            <strong>{generated}</strong>
          </span>
          <span>
            <b>{t("localSource")}</b>
            <strong>{sourceState}</strong>
          </span>
          <span>
            <b>{t("projectCounts")}</b>
            <strong>{dashboardProjectMeta(t, snapshot)}</strong>
          </span>
        </div>
      </div>
    </section>
  );
}

function DashboardBandHead({ kicker, title, meta }: { kicker: string; title: string; meta?: string }) {
  return (
    <div className="dash-band-head">
      <div>
        <span className="note-kicker">{kicker}</span>
        <h2>{title}</h2>
      </div>
      {meta ? <span>{meta}</span> : null}
    </div>
  );
}

function DashboardInspectorStrip({
  t,
  activeTab,
  setActiveTab,
  query,
  setQuery,
  items,
  selection,
  setSelection,
}: {
  t: (key: string) => string;
  activeTab: RailTab;
  setActiveTab: (tab: RailTab) => void;
  query: string;
  setQuery: (value: string) => void;
  items: RailItem[];
  selection: Selection;
  setSelection: (value: Selection) => void;
}) {
  const [showOverflow, setShowOverflow] = useState(false);
  useEffect(() => {
    setShowOverflow(false);
  }, [activeTab, query]);
  const hiddenCount = Math.max(0, items.length - INSPECTOR_INITIAL_LIMIT);
  const visibleItems = showOverflow ? items : items.slice(0, INSPECTOR_INITIAL_LIMIT);
  const overflowLabel = countLabel(t, showOverflow ? "lessCount" : "moreCount", hiddenCount);
  return (
    <section className="dash-inspector-strip" aria-label={t("inspect")}>
      <div className="dash-inspector-title">
        <span className="note-kicker">{t("inspect")}</span>
        <strong>{t("projects")} / {t("sessions")} / {t("processes")}</strong>
      </div>
      <div className="dash-inspector-controls">
        <div className="rail-tabs" role="tablist">
          {(["projects", "sessions", "processes"] as RailTab[]).map((tab) => (
            <button key={tab} className={`rail-tab ${activeTab === tab ? "is-active" : ""}`} type="button" role="tab" data-focus-key={focusKey("dashboard-inspector-tab", tab)} onClick={() => setActiveTab(tab)}>
              {tab === "projects" ? <GitBranch size={14} /> : tab === "sessions" ? <Bot size={14} /> : <Server size={14} />}
              {t(tab)}
            </button>
          ))}
        </div>
        <div className="rail-search">
          <Search size={15} />
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("search")} autoComplete="off" spellCheck={false} />
        </div>
      </div>
      <div className="dash-inspector-list">
        <button className={`ledger-chip overview ${selection.type === "overview" ? "is-selected" : ""}`} type="button" data-focus-key={focusKey("dashboard-inspector-item", "overview")} onClick={() => setSelection({ type: "overview", id: "overview" })}>
          <span>{t("overview")}</span>
          <em>/api</em>
        </button>
        {visibleItems.map((item) => (
          <button
            className={`ledger-chip ${selection.id === item.id && selection.type === item.type ? "is-selected" : ""}`}
            type="button"
            key={`${item.type}-${item.id}`}
            data-focus-key={focusKey("dashboard-inspector-item", item.type, item.id)}
            onClick={() => setSelection({ type: item.type, id: item.id } as Selection)}
          >
            <span>{item.title}</span>
            <em>{item.value}</em>
          </button>
        ))}
        {hiddenCount ? (
          <button className={`session-tree-more inspector-more ${showOverflow ? "is-expanded" : ""}`} type="button" data-focus-key={focusKey("dashboard-inspector-more", activeTab)} onClick={() => setShowOverflow((value) => !value)} aria-expanded={showOverflow} aria-label={overflowLabel} title={overflowLabel}>
            <ChevronDown size={12} aria-hidden="true" />
            <span>{overflowLabel}</span>
          </button>
        ) : null}
      </div>
    </section>
  );
}

function DashboardFrontTopline({
  t,
  snapshot,
  refreshInterval,
  cycleRefreshInterval,
}: {
  t: (key: string) => string;
  snapshot: Snapshot;
  refreshInterval: number;
  cycleRefreshInterval: () => void;
}) {
  const stats = snapshot.transcript_stats ?? {};
  return (
    <div className="dash-front-topline">
      <div className="dash-front-status">
        <span className={`field-status ${statusTone(snapshot)}`}>{metricState(snapshot, t)}</span>
        <span className="issue-stamp">{coordinationPostureLabel(snapshot, t)}</span>
      </div>
      <div className="dash-front-meta">
        <div className="dash-front-meta-item stamp">
          <span>{t("observed")}</span>
          <strong>{snapshot.generated_at ? formatDateTime(snapshot.generated_at) : t("unavailable")}</strong>
          <button className={`refresh-interval front-refresh-interval ${refreshInterval ? "" : "is-paused"}`} type="button" data-focus-key={focusKey("refresh-interval", "front")} onClick={cycleRefreshInterval} title={t("autoRefresh")} aria-label={t("autoRefresh")}>
            <RefreshCw size={10} aria-hidden="true" />
            <span>{formatRefreshInterval(refreshInterval, t)}</span>
          </button>
        </div>
        <div className="dash-front-meta-item">
          <span>{t("localSource")}</span>
          <strong>{stats.cached ? t("cached") : t("fresh")}</strong>
          <em>{transcriptScanNote(t, stats)}</em>
        </div>
        <div className="dash-front-meta-item">
          <span>{t("projectCounts")}</span>
          <strong>{dashboardProjectMeta(t, snapshot)}</strong>
          <em>{dashboardProjectLead(t, snapshot)}</em>
        </div>
      </div>
    </div>
  );
}

function DashboardFieldGrid({ t, snapshot }: { t: (key: string) => string; snapshot: Snapshot }) {
  const current = snapshot.current ?? {};
  const summary = snapshot.summary ?? {};
  const active = current.active_burst_concurrency ?? 0;
  const sessions = current.session_concurrency ?? 0;
  const mapped = summary.mapped_processes ?? 0;
  const unmatched = summary.unmapped_processes ?? 0;
  const pids = current.pid_concurrency ?? 0;
  const coverage = clampPct(summary.mapping_coverage_pct ?? 0);
  const resources = processResourceTotals(snapshot.live_processes);
  return (
    <>
      <div className="dash-field-grid">
        <article className="dash-support-cell burst">
          <span><TermLabel label={t("metricFresh")} tip={t("tipActiveBurst")} /></span>
          <strong>{active}</strong>
          <em>{t("activeBurstHint")}</em>
        </article>
        <article className="dash-support-cell session">
          <span><TermLabel label={t("metricKnownSessions")} tip={t("tipSessions")} /></span>
          <strong>{sessions}</strong>
          <em>{t("liveIdle").replace("{live}", String(summary.active_sessions ?? 0)).replace("{idle}", String(summary.idle_sessions ?? 0))}</em>
        </article>
        <article className="dash-support-cell mapping">
          <span><TermLabel label={t("mappingHealth")} tip={t("tipMappingHealth")} /></span>
          <strong>{formatPct(summary.mapping_coverage_pct)}</strong>
          <em>{`${mapped} ${t("mapped")} / ${unmatched} ${t("unmatched")}`}</em>
        </article>
      </div>
      <div className="dash-process-diagnostic" aria-label={t("processPressure")}>
        <span><Server size={12} aria-hidden="true" />{t("processPressure")}</span>
        <strong>{pids}</strong>
        <em>{`${processResourceText(t, resources.cpu, resources.memory)} · ${formatCopy(t("processDiagnosticFormula"), { pids, mapped, unmatched })}`}</em>
        <i aria-hidden="true"><b style={{ width: `${coverage}%` }} /></i>
      </div>
    </>
  );
}

function ErrorBanner({ t, error, compact = false }: { t: (key: string) => string; error: string | null; compact?: boolean }) {
  if (!error) return null;
  return (
    <div className={`warning-banner snapshot-error ${compact ? "compact" : ""}`} role="status" aria-live="polite">
      <span>{t("failedSnapshot").replace("{message}", error)}</span>
    </div>
  );
}

function EmptySurface({ t, compact = false, error = null }: { t: (key: string) => string; compact?: boolean; error?: string | null }) {
  const title = error ? t("noCurrentSnapshotTitle") : t("noData");
  const detail = error ? t("noCurrentSnapshotDetail") : t("emptySub");
  return (
    <main className={compact ? "popover-surface" : "dashboard-surface"}>
      <ErrorBanner t={t} error={error} compact={compact} />
      <section className="empty-pane">
        <div className="empty-glyph"><Terminal size={34} /></div>
        <p className="empty-title">{title}</p>
        <p className="empty-sub">{detail}</p>
      </section>
    </main>
  );
}

function BandHead({ kicker, title, meta }: { kicker: string; title: string; meta?: string }) {
  return (
    <div className="band-head">
      <div>
        <span>{kicker}</span>
        <h2>{title}</h2>
      </div>
      {meta ? <em>{meta}</em> : null}
    </div>
  );
}

function FieldIndex({ t, snapshot, compact = false }: { t: (key: string) => string; snapshot: Snapshot; compact?: boolean }) {
  const current = snapshot.current ?? {};
  const summary = snapshot.summary ?? {};
  const scale = currentPeerScale(current);
  const items = [
    { key: "burst", label: t("metricFresh"), tip: t("tipActiveBurst"), value: current.active_burst_concurrency ?? 0, detail: t("active"), tone: "burst", pct: pctPart(current.active_burst_concurrency, scale) },
    { key: "sessions", label: t("metricSessions"), tip: t("tipSessions"), value: current.session_concurrency ?? 0, detail: `${summary.active_sessions ?? 0} ${t("active")} · ${summary.idle_sessions ?? 0} ${t("idle")}`, tone: "session", pct: pctPart(current.session_concurrency, scale) },
    { key: "pids", label: t("metricProcesses"), tip: t("tipPids"), value: current.pid_concurrency ?? 0, detail: `${summary.mapped_processes ?? 0} ${t("mapped")} · ${summary.unmapped_processes ?? 0} ${t("unmatched")}`, tone: "pid", pct: pctPart(current.pid_concurrency, scale) },
  ];
  return (
    <section className={`field-index ${compact ? "compact" : ""}`}>
      <BandHead kicker={t("runtimeField")} title={t("activityCounts")} meta={`${formatPct(summary.mapping_coverage_pct)} ${t("coverage")}`} />
      <div className="field-grid">
        {items.map((item) => (
          <article className={`field-cell ${item.tone}`} key={item.key}>
            <span><TermLabel label={item.label} tip={item.tip} /></span>
            <strong>{item.value}</strong>
            <em>{item.detail}</em>
            <i aria-hidden="true"><b style={{ width: `${clampPct(item.pct, 4)}%` }} /></i>
          </article>
        ))}
      </div>
    </section>
  );
}

function PopoverAuditShell({
  t,
  snapshot,
  selection,
  setSelection,
  setHoverDetail,
}: {
  t: (key: string) => string;
  snapshot: Snapshot;
  selection: Selection;
  setSelection: (value: Selection) => void;
  setHoverDetail: HoverDetailSink;
}) {
  return (
    <section className="popover-panel audit-shell">
      <PopoverRuntimeInstrument t={t} snapshot={snapshot} />
      <ScanBoundary t={t} snapshot={snapshot} compact />
      <section className="popover-project-table">
        <div className="popover-project-head">
          <div>
            <span className="note-kicker">{t("liveLedger")}</span>
            <h2>{t("projectSessionTree")}</h2>
          </div>
          <span>{dashboardProjectMeta(t, snapshot)}</span>
        </div>
        <ProjectAtlas t={t} snapshot={snapshot} selection={selection} setSelection={setSelection} compact defaultExpandedCount={0} showHead={false} setHoverDetail={setHoverDetail} />
      </section>
    </section>
  );
}

function PopoverHoverInspector({ t, detail }: { t: (key: string) => string; detail: HoverDetailState | null }) {
  const payload = detail?.detail ?? null;
  const style = detail ? hoverInspectorStyle(detail) : undefined;
  return (
    <aside className={`popover-hover-inspector ${payload ? `${detail?.visible ? "is-visible" : ""} ${payload.kind}` : ""}`} style={style} aria-hidden={payload ? "false" : "true"} aria-live="polite">
      {payload ? (
        <>
          <span className="hover-inspector-mark" aria-hidden="true">
            {payload.kind === "session" ? <Bot size={14} /> : <Layers size={14} />}
          </span>
          <span className="hover-inspector-body">
            {payload.kind === "session" ? (
              <SessionHoverContent t={t} title={payload.title} metrics={payload.metrics} tokenParts={payload.tokenParts} meta={payload.meta} />
            ) : (
              <>
                <strong>{payload.title}</strong>
                <em>{payload.detail}</em>
                {payload.meta ? <small>{payload.meta}</small> : null}
              </>
            )}
          </span>
        </>
      ) : null}
    </aside>
  );
}

function hoverPointFromEvent(event?: HoverDetailEvent): { x: number; y: number } {
  if (event && "clientX" in event && event.clientX && event.clientY) {
    return { x: event.clientX, y: event.clientY };
  }
  const rect = event?.currentTarget.getBoundingClientRect();
  if (rect) {
    return {
      x: rect.left + Math.min(Math.max(rect.width * 0.66, 24), Math.max(rect.width - 18, 24)),
      y: rect.top + Math.min(Math.max(rect.height * 0.55, 14), Math.max(rect.height - 8, 14)),
    };
  }
  return { x: 24, y: 24 };
}

function hoverInspectorStyle(state: HoverDetailState): React.CSSProperties {
  const viewportWidth = typeof window === "undefined" ? 420 : window.innerWidth;
  const viewportHeight = typeof window === "undefined" ? 560 : window.innerHeight;
  const width = state.detail.kind === "session"
    ? Math.min(348, Math.max(244, viewportWidth - 176))
    : Math.min(304, Math.max(220, viewportWidth - 196));
  const height = state.detail.kind === "session" && width < 280 ? 220 : state.detail.kind === "session" ? 156 : 86;
  const gap = 14;
  let left = state.x + gap;
  let top = state.y - height - gap;
  if (left + width > viewportWidth - 10) left = state.x - width - gap;
  if (top < 10) top = state.y + gap;
  if (top + height > viewportHeight - 10) top = state.y - height - gap;
  left = Math.max(10, Math.min(left, Math.max(10, viewportWidth - width - 10)));
  top = Math.max(10, Math.min(top, Math.max(10, viewportHeight - height - 10)));
  return {
    left,
    top,
    "--hover-width": `${width}px`,
  } as React.CSSProperties;
}

function PopoverRuntimeInstrument({ t, snapshot }: { t: (key: string) => string; snapshot: Snapshot }) {
  const current = snapshot.current ?? {};
  const summary = snapshot.summary ?? {};
  const trustedScale = Math.max(1, current.active_burst_concurrency ?? 0, current.session_concurrency ?? 0);
  const active = (current.active_burst_concurrency ?? 0) > 0;
  const mapped = summary.mapped_processes ?? 0;
  const unmatched = summary.unmapped_processes ?? 0;
  const pids = current.pid_concurrency ?? 0;
  const coverage = clampPct(summary.mapping_coverage_pct ?? 0);
  const resources = processResourceTotals(snapshot.live_processes);
  const rows = [
    {
      key: "burst",
      label: t("metricFresh"),
      tip: t("tipActiveBurst"),
      value: current.active_burst_concurrency ?? 0,
      detail: t("activeBurstHint"),
      pct: pctPart(current.active_burst_concurrency, trustedScale),
    },
    {
      key: "session",
      label: t("metricKnownSessions"),
      tip: t("tipSessions"),
      value: current.session_concurrency ?? 0,
      detail: t("liveIdle")
        .replace("{live}", String(summary.active_sessions ?? 0))
        .replace("{idle}", String(summary.idle_sessions ?? 0)),
      pct: pctPart(current.session_concurrency, trustedScale),
    },
    {
      key: "mapping",
      label: t("mappingHealth"),
      tip: t("tipMappingHealth"),
      value: formatPct(summary.mapping_coverage_pct),
      detail: `${mapped} ${t("mapped")} / ${unmatched} ${t("unmatched")}`,
      pct: coverage,
    },
  ];
  return (
    <section className={`popover-instrument ${active ? "is-active" : ""}`} aria-label={t("runtimeField")}>
      <div className="instrument-stat-grid">
        {rows.map((row) => (
          <article className={`instrument-stat ${row.key}`} key={row.key}>
            <span><TermLabel label={row.label} tip={row.tip} /></span>
            <strong>{row.value}</strong>
            <em>{row.detail}</em>
          </article>
        ))}
      </div>
      <div className="instrument-scale-rail" aria-label={t("calibration")}>
        {rows.map((row) => (
          <span className={`instrument-scale-row ${row.key}`} key={row.key}>
            <b><TermLabel label={row.label} tip={row.tip} /></b>
            <i aria-hidden="true"><em style={{ width: `${clampPct(row.pct, 3)}%` }} /></i>
          </span>
        ))}
      </div>
      <div className="process-diagnostic-strip" aria-label={t("processPressure")}>
        <span><Server size={12} aria-hidden="true" />{t("processPressure")}</span>
        <strong>{pids}</strong>
        <em>{`${processResourceText(t, resources.cpu, resources.memory)} · ${formatCopy(t("processDiagnosticFormula"), { pids, mapped, unmatched })}`}</em>
      </div>
      <CurrentMeaningStrip t={t} snapshot={snapshot} compact />
    </section>
  );
}

function CurrentMeaningStrip({ t, snapshot, compact = false }: { t: (key: string) => string; snapshot: Snapshot; compact?: boolean }) {
  const detailsId = useId();
  const [expanded, setExpanded] = useState(!compact);
  const lead = currentMeaningLead(t, snapshot);
  const points = currentMeaningPoints(t, snapshot).slice(0, compact ? 2 : 3);
  const stats = snapshot.transcript_stats ?? {};
  const activeWindow = activeWindowLabel(t, snapshot);
  return (
    <section className={`meaning-strip ${compact ? "compact" : ""} ${expanded ? "is-expanded" : ""}`}>
      <div className="meaning-head">
        <Activity size={15} />
        <strong>{t("currentMeaning")}</strong>
        <em title={`${t("activeDefinitionLabel")}: ${activeWindow}`}>{activeWindow}</em>
        <button
          className="disclosure-icon-btn"
          type="button"
          data-focus-key={focusKey("meaning-detail", compact ? "compact" : "full")}
          aria-label={expanded ? t("collapseDetails") : t("expandDetails")}
          aria-expanded={expanded}
          aria-controls={detailsId}
          onClick={() => setExpanded((value) => !value)}
          title={expanded ? t("collapseDetails") : t("expandDetails")}
        >
          <Info size={12} />
        </button>
      </div>
      <p>{lead}</p>
      <div className="meaning-detail-grid" id={detailsId} hidden={!expanded}>
        <span>
          <b>{t("metricExplanation")}</b>
          <em>{`${lead} ${t("metricLegend")} ${t("activeThinkingCaveat")}`}</em>
        </span>
        <span>
          <b>{t("evidenceNote")}</b>
          <em>{primaryEvidenceNote(t, snapshot)}</em>
        </span>
        <span>
          <b><TermLabel label={t("scanState")} tip={t("tipScanner")} /></b>
          <em>{`${transcriptScanSummary(t, stats)} · ${transcriptScanNote(t, stats)}`}</em>
        </span>
      </div>
      {points.length ? (
        <div className="meaning-point-row" hidden={!expanded}>
          {points.map((point) => <span key={point}>{point}</span>)}
        </div>
      ) : null}
    </section>
  );
}

function DashboardEvidenceColumn({ t, snapshot }: { t: (key: string) => string; snapshot: Snapshot }) {
  const stats = snapshot.transcript_stats ?? {};
  const summary = snapshot.summary ?? {};
  return (
    <aside className="dash-evidence-column">
      <DashboardBandHead kicker={t("evidenceColumn")} title={t("runtimeEvidence")} meta={t("scanAndMapping")} />
      <div className="dash-evidence-block">
        <div className="evidence-grid">
          <Readout label={t("scan")} value={`${stats.parsed_files ?? 0}/${stats.scanned_files ?? 0}`} />
          <Readout label={t("deferred")} value={deferredScanValue(t, stats)} />
          <Readout label={t("tail")} value={String(stats.tail_parsed_files ?? 0)} />
          <Readout label={t("metricMatched")} value={formatPct(summary.mapping_coverage_pct)} />
        </div>
        <EvidenceHealth t={t} snapshot={snapshot} />
      </div>
      <div className="dash-evidence-block tool-split">
        <div className="dash-mini-head">
          <h3>{t("toolSplit")}</h3>
          <span>{t("topLiveMix")}</span>
        </div>
        <ToolMix t={t} snapshot={snapshot} />
      </div>
    </aside>
  );
}

function EvidenceHealth({ t, snapshot }: { t: (key: string) => string; snapshot: Snapshot }) {
  const stats = snapshot.transcript_stats ?? {};
  const summary = snapshot.summary ?? {};
  const current = snapshot.current ?? {};
  const coverage = clampPct(summary.mapping_coverage_pct ?? 0);
  const tone = coverage >= 80 ? "good" : coverage >= 50 ? "warn" : "bad";
  return (
    <section className={`evidence-health ${tone}`} aria-label={t("evidenceHealth")}>
      <article className="evidence-note mapping-note">
        <div className="evidence-note-head">
          <span><TermLabel label={t("mappingHealth")} tip={t("tipMappingHealth")} /></span>
          <strong>{formatPct(summary.mapping_coverage_pct)}</strong>
        </div>
        <div className="mapping-meter" style={{ "--coverage": `${coverage}%` } as React.CSSProperties}>
          <i />
        </div>
        <p>{mappingHealthText(t, snapshot)}</p>
      </article>
      <article className="evidence-note scanner-note">
        <div className="evidence-note-head">
          <span><TermLabel label={t("scanState")} tip={t("tipScanner")} /></span>
          <strong>{stats.cached ? t("cached") : t("fresh")}</strong>
        </div>
        <p>{transcriptScanSummary(t, stats, snapshot.history?.retained_sample_count)}</p>
        <p>{transcriptScanNote(t, stats)}</p>
      </article>
      <article className="evidence-note signal-note">
        <div className="evidence-note-head">
          <span>{t("evidenceNote")}</span>
          <strong>{coordinationPostureLabel(snapshot, t)}</strong>
        </div>
        <p>{primaryEvidenceNote(t, snapshot)}</p>
        <em>{current.pid_concurrency ?? 0} {t("processesObserved")}</em>
      </article>
    </section>
  );
}

function ProjectAtlas({
  t,
  snapshot,
  selection,
  setSelection,
  compact = false,
  limit,
  defaultExpandedCount,
  showHead = true,
  setHoverDetail,
}: {
  t: (key: string) => string;
  snapshot: Snapshot;
  selection: Selection;
  setSelection: (value: Selection) => void;
  compact?: boolean;
  limit?: number;
  defaultExpandedCount: number;
  showHead?: boolean;
  setHoverDetail?: HoverDetailSink;
}) {
  const allProjects = useMemo(() => orderedProjects(snapshot), [snapshot]);
  const clippedProjects = allProjects.slice(0, limit ?? Number.POSITIVE_INFINITY);
  const hiddenProjectCount = Math.max(0, allProjects.length - clippedProjects.length);
  const [showOverflow, setShowOverflow] = useState(false);
  const overflowLabel = countLabel(t, showOverflow ? "lessCount" : "moreCount", hiddenProjectCount);
  const projects = showOverflow ? allProjects : clippedProjects;
  const { openProjects, openProject, toggleProject } = useProjectDisclosure(allProjects, defaultExpandedCount);
  return (
    <section className={`project-atlas ${compact ? "compact" : ""}`}>
      {showHead ? <BandHead kicker={compact ? t("liveLedger") : t("projects")} title={t("projectSessionTree")} meta={`${allProjects.length} ${t("projects")}`} /> : null}
      {projects.length ? (
        <div className="project-columns" aria-hidden="true">
          <span>#</span>
          <span>{t("projects")}</span>
          <span>{t("sessions")}</span>
          <span>{t("tools")}</span>
        </div>
      ) : null}
      <div className="project-tree-list">
        {projects.length ? projects.map((project, index) => {
          const projectId = safeID(project.project);
          return (
            <ProjectTreeRow
              key={projectId}
              t={t}
              snapshot={snapshot}
              project={project}
              selection={selection}
              setSelection={setSelection}
              compact={compact}
              expanded={openProjects.has(projectId)}
              onToggle={() => toggleProject(projectId)}
              onOpen={() => openProject(projectId)}
              rank={index + 1}
              setHoverDetail={setHoverDetail}
            />
          );
        }) : (
          <section className="empty-inline">
            <Layers size={18} />
            <span>{t("noData")}</span>
          </section>
        )}
        {hiddenProjectCount ? (
          <button className={`session-tree-more project-tree-more ${showOverflow ? "is-expanded" : ""}`} type="button" onClick={() => setShowOverflow((value) => !value)} aria-expanded={showOverflow} aria-label={overflowLabel} title={overflowLabel}>
            <ChevronDown size={12} aria-hidden="true" />
            <span>{overflowLabel}</span>
          </button>
        ) : null}
      </div>
    </section>
  );
}

function DashboardSideRails({ t, snapshot }: { t: (key: string) => string; snapshot: Snapshot }) {
  return (
    <aside className="dash-atlas-side">
      <section className="dash-side-module">
        <div className="dash-mini-head"><h3>{t("calibration")}</h3><span>{t("currentMeaning")}</span></div>
        <CalibrationRail t={t} snapshot={snapshot} />
      </section>
      <section className="dash-side-module">
        <div className="dash-mini-head"><h3>{t("candidateWorkitems")}</h3><span>{t("sessionProcessMix")}</span></div>
        <CandidateWorkitemsRail t={t} snapshot={snapshot} limit={3} />
      </section>
      <section className="dash-side-module">
        <div className="dash-mini-head"><h3>{t("sessionAge")}</h3><span>{t("currentFreshnessBuckets")}</span></div>
        <AgeRail t={t} buckets={snapshot.age_buckets ?? []} />
      </section>
      <section className="dash-side-module">
        <div className="dash-mini-head"><h3>{t("evidenceConfidence")}</h3><span>{t("confidence")}</span></div>
        <ConfidenceGrid t={t} snapshot={snapshot} />
      </section>
    </aside>
  );
}

function CalibrationRail({ t, snapshot }: { t: (key: string) => string; snapshot: Snapshot }) {
  const current = snapshot.current ?? {};
  const summary = snapshot.summary ?? {};
  const trustedScale = Math.max(1, current.active_burst_concurrency ?? 0, current.session_concurrency ?? 0);
  const mapped = summary.mapped_processes ?? 0;
  const unmatched = summary.unmapped_processes ?? 0;
  const pids = current.pid_concurrency ?? 0;
  const coverage = clampPct(summary.mapping_coverage_pct ?? 0);
  const rows = [
    {
      key: "burst",
      label: t("active"),
      value: current.active_burst_concurrency ?? 0,
      primary: t("activeBurstHint"),
      secondary: t("currentScale"),
      pct: pctPart(current.active_burst_concurrency, trustedScale),
    },
    {
      key: "session",
      label: t("metricKnownSessions"),
      value: current.session_concurrency ?? 0,
      primary: t("knownSessions"),
      secondary: t("liveIdle")
        .replace("{live}", String(summary.active_sessions ?? 0))
        .replace("{idle}", String(summary.idle_sessions ?? 0)),
      pct: pctPart(current.session_concurrency, trustedScale),
    },
    {
      key: "mapping",
      label: t("mappingHealth"),
      value: formatPct(summary.mapping_coverage_pct),
      primary: t("processEvidenceMapped"),
      secondary: `${mapped} ${t("mapped")} · ${unmatched} ${t("unmatched")}`,
      pct: coverage,
    },
  ];
  return (
    <div className="calibration-rail">
      {rows.map((row) => (
        <article className={`calibration-row ${row.key}`} key={row.key}>
          <div className="calibration-row-head">
            <span>{row.label}</span>
            <strong>{row.value}</strong>
          </div>
          <i aria-hidden="true"><b style={{ width: `${clampPct(row.pct, 4)}%` }} /></i>
          <p>{row.primary}</p>
          <em>{row.secondary}</em>
        </article>
      ))}
      <article className="calibration-row process-diagnostic">
        <div className="calibration-row-head">
          <span>{t("processPressure")}</span>
          <strong>{pids}</strong>
        </div>
        <i aria-hidden="true"><b style={{ width: `${coverage}%` }} /></i>
        <p>{t("visibleProcesses")}</p>
        <em>{formatCopy(t("processDiagnosticFormula"), { pids, mapped, unmatched })}</em>
      </article>
    </div>
  );
}

function AgeRail({ t, buckets }: { t: (key: string) => string; buckets: AgeBucketSnapshot[] }) {
  const max = Math.max(1, ...buckets.map((bucket) => bucket.count ?? 0));
  return (
    <div className="age-rail">
      {buckets.length ? buckets.map((bucket) => (
        <div className="age-row" key={bucket.label || "bucket"}>
          <span>{bucket.label || t("unavailable")}</span>
          <i><b style={{ width: `${clampPct(((bucket.count ?? 0) / max) * 100, 3)}%` }} /></i>
          <strong>{bucket.count ?? 0}</strong>
        </div>
      )) : <span className="muted-inline">{t("unavailable")}</span>}
    </div>
  );
}

function ConfidenceGrid({ t, snapshot }: { t: (key: string) => string; snapshot: Snapshot }) {
  const fromProjects = (snapshot.project_focus ?? []).flatMap((project) => project.confidence_breakdown ?? []);
  const counts = new Map<string, number>();
  fromProjects.forEach((item) => {
    const level = confidenceLabel(t, item.level);
    counts.set(level, (counts.get(level) ?? 0) + (item.count ?? 0));
  });
  if (!counts.size) {
    (snapshot.live_sessions ?? []).forEach((session) => {
      const level = confidenceLabel(t, session.confidence);
      counts.set(level, (counts.get(level) ?? 0) + 1);
    });
  }
  const risk = snapshot.coordination_risk ?? {};
  const summary = snapshot.summary ?? {};
  const facts = [
    { label: t("candidateCoverage"), value: formatPct(risk.candidate_workitem_coverage_pct), warn: false },
    { label: t("lowConfidence"), value: String(risk.low_confidence_session_count ?? 0), warn: (risk.low_confidence_session_count ?? 0) > 0 },
    { label: t("stale"), value: String(risk.stale_session_count ?? 0), warn: (risk.stale_session_count ?? 0) > 0 },
    { label: t("unmatched"), value: String(risk.orphan_process_count ?? summary.unmapped_processes ?? 0), warn: (risk.orphan_process_count ?? summary.unmapped_processes ?? 0) > 0 },
  ];
  return (
    <div className="confidence-grid">
      {facts.map((fact) => (
        <span className={fact.warn ? "warn" : ""} key={fact.label}><b>{fact.label}</b><strong>{fact.value}</strong></span>
      ))}
      {Array.from(counts.entries()).map(([level, count]) => (
        <span key={level}><b>{level}</b><strong>{count}</strong></span>
      ))}
    </div>
  );
}

function useProjectDisclosure(projects: ProjectSnapshot[], defaultExpandedCount: number) {
  const projectIds = useMemo(() => projects.map((project) => safeID(project.project)), [projects]);
  const projectIdKey = projectIds.join("\u0000");
  const initialOpen = useMemo(() => new Set(projectIds.slice(0, defaultExpandedCount)), [defaultExpandedCount, projectIdKey]);
  const seededRef = useRef(projectIds.length > 0);
  const [openProjects, setOpenProjects] = useState<Set<string>>(() => new Set(initialOpen));

  useEffect(() => {
    const knownProjects = new Set(projectIds);
    setOpenProjects((current) => {
      if (!seededRef.current && projectIds.length) {
        seededRef.current = true;
        return new Set(initialOpen);
      }
      let changed = false;
      const next = new Set<string>();
      current.forEach((id) => {
        if (knownProjects.has(id)) next.add(id);
        else changed = true;
      });
      return changed ? next : current;
    });
  }, [initialOpen, projectIdKey, projectIds]);

  const openProject = useCallback((id: string) => {
    if (!id) return;
    setOpenProjects((current) => {
      if (current.has(id)) return current;
      const next = new Set(current);
      next.add(id);
      return next;
    });
  }, []);

  const toggleProject = useCallback((id: string) => {
    if (!id) return;
    setOpenProjects((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  return { openProjects, openProject, toggleProject };
}

function CandidateWorkitemsRail({ t, snapshot, limit = 5 }: { t: (key: string) => string; snapshot: Snapshot; limit?: number }) {
  const rows = [...(snapshot.candidate_workitems ?? [])]
    .sort((a, b) => (b.session_count ?? 0) - (a.session_count ?? 0))
    .slice(0, limit);
  if (!rows.length) return <span className="muted-inline">{t("unavailable")}</span>;
  return (
    <div className="candidate-rail">
      {rows.map((item, index) => (
        <article className="candidate-row" key={item.key || `${item.project}-${item.tool}-${index}`}>
          <div className="candidate-main">
            <ToolIcon t={t} tool={item.tool || "unknown"} />
            <span>
              <strong>{item.project || t("unassigned")}</strong>
              <em>{freshnessLabel(t, item.freshness_bucket)} · {confidenceLabel(t, item.confidence)}</em>
            </span>
          </div>
          <div className="candidate-mix" title={t("sessionProcessMix")}>
            <b>{item.session_count ?? 0}</b>
            <i>/</i>
            <b>{item.process_count ?? 0}</b>
          </div>
        </article>
      ))}
    </div>
  );
}

function ProcessLedger({ t, snapshot, selection, setSelection }: { t: (key: string) => string; snapshot: Snapshot; selection: Selection; setSelection: (value: Selection) => void }) {
  const [showOverflow, setShowOverflow] = useState(false);
  const [filter, setFilter] = useState<ProcessFilter>({ kind: "all", id: "all" });
  const rows = useMemo(() => [...(snapshot.live_processes ?? [])].sort((a, b) => {
    if ((b.mapped_active_sessions ?? 0) !== (a.mapped_active_sessions ?? 0)) return (b.mapped_active_sessions ?? 0) - (a.mapped_active_sessions ?? 0);
    if ((b.mapped_sessions ?? 0) !== (a.mapped_sessions ?? 0)) return (b.mapped_sessions ?? 0) - (a.mapped_sessions ?? 0);
    return (a.pid ?? 0) - (b.pid ?? 0);
  }), [snapshot.live_processes]);
  const filteredRows = useMemo(() => rows.filter((process) => processMatchesFilter(process, filter)), [filter, rows]);
  useEffect(() => {
    setShowOverflow(false);
  }, [filter.kind, filter.id]);
  const hiddenCount = Math.max(0, filteredRows.length - PROCESS_LEDGER_INITIAL_LIMIT);
  const visibleRows = showOverflow ? filteredRows : filteredRows.slice(0, PROCESS_LEDGER_INITIAL_LIMIT);
  const overflowLabel = countLabel(t, showOverflow ? "lessCount" : "moreCount", hiddenCount);
  return (
    <div className="process-ledger-shell">
      <ProcessAuditFilters t={t} snapshot={snapshot} filter={filter} setFilter={setFilter} />
      <div className="process-filter-readout">
        <span>{processFilterLabel(t, filter)}</span>
        <em>{filteredRows.length}/{rows.length} {t("processes")}</em>
      </div>
      <div className="process-ledger" role="table" aria-label={t("processLedger")}>
      <div className="process-row head" role="row">
        <span>{t("processes")}</span>
        <span>{t("tools")}</span>
        <span>{t("roleMix")}</span>
        <span>{t("sessions")}</span>
        <span>{t("resources")}</span>
        <span>{t("host")}</span>
      </div>
      {visibleRows.map((process) => (
        <ProcessLedgerRow t={t} process={process} selection={selection} setSelection={setSelection} key={process.pid ?? process.command} />
      ))}
      {hiddenCount ? (
        <div className="process-ledger-more-row" role="row">
          <button className={`session-tree-more process-ledger-more ${showOverflow ? "is-expanded" : ""}`} type="button" onClick={() => setShowOverflow((value) => !value)} aria-expanded={showOverflow} aria-label={overflowLabel} title={overflowLabel}>
            <ChevronDown size={12} aria-hidden="true" />
            <span>{overflowLabel}</span>
          </button>
        </div>
      ) : null}
      </div>
    </div>
  );
}

function ProcessLedgerRow({ t, process, selection, setSelection }: { t: (key: string) => string; process: LiveProcess; selection: Selection; setSelection: (value: Selection) => void }) {
  const processID = String(process.pid ?? "");
  const sessions = process.session_ids ?? [];
  const evidence = process.mapped_session_evidence ?? [];
  const [showAllSessions, setShowAllSessions] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const host = process.host_app;
  const selected = selection.type === "process" && selection.id === processID;
  const hiddenSessionCount = Math.max(0, sessions.length - 3);
  const visibleSessions = showAllSessions ? sessions : sessions.slice(0, 3);
  const sessionOverflowLabel = countLabel(t, showAllSessions ? "lessCount" : "moreCount", hiddenSessionCount);
  const processHoverDetail = `${processResourceText(t, process.cpu_percent, process.memory_bytes)} · ${t("runtimeDuration")} ${process.elapsed || t("unavailable")} · ${process.mapped_sessions ?? 0} ${t("mappedSessions")}`;
  const processHoverMeta = process.command || t("unavailable");
  return (
    <div className={`process-row process-detail-row ${selected ? "is-selected" : ""} ${expanded ? "is-expanded" : ""}`} role="row">
      <span className="process-cell process-main-cell" role="cell">
        <button className="process-expand" type="button" onClick={() => setExpanded((value) => !value)} aria-expanded={expanded} aria-label={expanded ? t("collapseDetails") : t("expandDetails")}>
          <ChevronDown size={12} aria-hidden="true" />
        </button>
        <button className="process-main" type="button" data-focus-key={focusKey("process", processID)} aria-current={selected ? "true" : undefined} onClick={() => setSelection({ type: "process", id: processID })}>
          <Server size={13} />
          <span className="process-name">{processIdentity(process, t)}</span>
          <em>{t("pid")} {process.pid ?? t("unavailable")}</em>
        </button>
      </span>
      <span className="process-cell tool-cell" role="cell"><ToolIcon t={t} tool={process.tool || "unknown"} />{toolDisplayName(process.tool)}</span>
      <span className="process-role-mix" role="cell" aria-label={process.evidence_summary || t("roleMix")}>
        <ProcessRolePill label={t("mainShort")} value={process.main_sessions ?? 0} tone="main" />
        <ProcessRolePill label={t("subagentShort")} value={process.subagent_sessions ?? 0} tone="subagent" />
        <ProcessRolePill label={t("unknown")} value={process.unknown_role_sessions ?? 0} tone="unknown" />
        {(process.mapped_sessions ?? 0) === 0 ? <ProcessRolePill label={t("unmatched")} value={1} tone="unmapped" /> : null}
      </span>
      <div className={`process-session-preview ${showAllSessions ? "is-expanded" : ""}`} role="cell" aria-label={t("sessions")}>
        {sessions.length ? visibleSessions.map((sessionID) => (
          <button className="session-chip" type="button" key={sessionID} data-focus-key={focusKey("process-session", processID, sessionID)} onClick={() => setSelection({ type: "session", id: safeID(sessionID) })}>
            <span>{shortID(sessionID)}</span>
          </button>
        )) : <span className="muted-inline">{t("unavailable")}</span>}
        {hiddenSessionCount ? (
          <button
            className={`session-chip more ${showAllSessions ? "is-expanded" : ""}`}
            type="button"
            onClick={() => setShowAllSessions((value) => !value)}
            aria-expanded={showAllSessions}
            aria-label={sessionOverflowLabel}
          >
            {showAllSessions ? countLabel(t, "lessCount", hiddenSessionCount) : `+${hiddenSessionCount}`}
          </button>
        ) : null}
      </div>
      <span className={`process-resource-cell ${(process.cpu_percent ?? 0) > 0 ? "is-hot" : ""}`} role="cell" aria-label={`${processResourceText(t, process.cpu_percent, process.memory_bytes)} · ${process.elapsed || t("unavailable")}`}>
        <b>{formatMemory(process.memory_bytes, t)}</b>
        <em>{formatCPU(process.cpu_percent)} {t("cpu")}</em>
      </span>
      <span className="process-host-cell" role="cell">
        {host ? <HostAppButton t={t} host={host} /> : null}
        <span>{host?.name || t("unavailable")}</span>
      </span>
      <RowHoverDetail title={`${processIdentity(process, t)} · ${t("pid")} ${process.pid ?? t("unavailable")}`} detail={processHoverDetail} meta={processHoverMeta} />
      {expanded ? (
        <div className="process-row-details" role="cell">
          <div className="process-resource-detail">
            <span>{t("resources")}</span>
            <strong>{processResourceText(t, process.cpu_percent, process.memory_bytes)}</strong>
            <em>{t("runtimeDuration")} {process.elapsed || t("unavailable")} · {process.mapped_sessions ?? 0} {t("mappedSessions")} / {process.mapped_active_sessions ?? 0} {t("activeShort")}</em>
          </div>
          <div className="process-command-full">
            <span>{t("command")}</span>
            <code>{process.command || t("unavailable")}</code>
          </div>
          <div className="process-evidence-lines">
            {evidence.length ? evidence.map((item) => (
              <button className="process-evidence-line" type="button" key={item.session_id || `${processID}-evidence`} onClick={() => setSelection({ type: "session", id: safeID(item.session_id) })}>
                <span>{roleLabel(t, normalizedRole(item.role))}</span>
                <strong>{item.project || shortID(item.session_id) || t("unassigned")}</strong>
                <em>{mappingMethodLabel(t, item.mapping_method)} · {freshnessLabel(t, item.freshness)} · {confidenceLabel(t, item.confidence)}</em>
              </button>
            )) : <span className="muted-inline">{t("unmappedProcessDetail")}</span>}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function ProcessAuditFilters({ t, snapshot, filter, setFilter }: { t: (key: string) => string; snapshot: Snapshot; filter: ProcessFilter; setFilter: (filter: ProcessFilter) => void }) {
  const runtime = snapshot.runtime_process_summary ?? [];
  const hosts = snapshot.host_app_process_summary ?? [];
  const rows = snapshot.live_processes ?? [];
  const roleFilters: ProcessFilter[] = [
    { kind: "role", id: "direct" },
    { kind: "role", id: "subagent" },
    { kind: "role", id: "unknown" },
    { kind: "role", id: "unmapped" },
  ];
  return (
    <div className="process-audit-filters" aria-label={t("processFilters")}>
      <button className={`process-filter-chip ${isSameProcessFilter(filter, { kind: "all", id: "all" }) ? "is-selected" : ""}`} type="button" onClick={() => setFilter({ kind: "all", id: "all" })}>
        <span>{t("all")}</span>
        <strong>{rows.length}</strong>
      </button>
      {runtime.map((item) => (
        <button className={`process-filter-chip runtime ${isSameProcessFilter(filter, { kind: "runtime", id: item.key || item.tool || "" }) ? "is-selected" : ""}`} type="button" key={`runtime-${item.key || item.tool}`} onClick={() => setFilter({ kind: "runtime", id: item.key || item.tool || "" })}>
          <span>{toolDisplayName(item.display_name || item.tool)}</span>
          <strong>{item.pid_count ?? 0}</strong>
          <em>{`${processResourceText(t, item.cpu_percent, item.memory_bytes)} · ${processSummaryMix(t, item.direct_sessions, item.subagent_sessions, item.unknown_role_sessions, item.unmapped_processes)}`}</em>
        </button>
      ))}
      {hosts.slice(0, 4).map((item) => (
        <button className={`process-filter-chip host ${isSameProcessFilter(filter, { kind: "host", id: item.key || item.name || "" }) ? "is-selected" : ""}`} type="button" key={`host-${item.key || item.name}`} onClick={() => setFilter({ kind: "host", id: item.key || item.name || "" })}>
          <span>{item.name || t("hostUnknown")}</span>
          <strong>{item.pid_count ?? 0}</strong>
          <em>{`${processResourceText(t, item.cpu_percent, item.memory_bytes)} · ${processSummaryMix(t, item.direct_sessions, item.subagent_sessions, item.unknown_role_sessions, item.unmapped_processes)}`}</em>
        </button>
      ))}
      {roleFilters.map((item) => (
        <button className={`process-filter-chip role ${isSameProcessFilter(filter, item) ? "is-selected" : ""}`} type="button" key={`role-${item.id}`} onClick={() => setFilter(item)}>
          <span>{processRoleFilterName(t, item.id)}</span>
        </button>
      ))}
    </div>
  );
}

function ProcessRolePill({ label, value, tone }: { label: string; value: number; tone: string }) {
  return <span className={`process-role-pill ${tone} ${value > 0 ? "has-value" : ""}`}><b>{value}</b><em>{label}</em></span>;
}

function processResourceTotals(processes: LiveProcess[] = []): { cpu: number; memory: number } {
  return processes.reduce((total, process) => ({
    cpu: total.cpu + (process.cpu_percent ?? 0),
    memory: total.memory + (process.memory_bytes ?? 0),
  }), { cpu: 0, memory: 0 });
}

function processResourceText(t: (key: string) => string, cpu?: number, memory?: number): string {
  return `${formatCPU(cpu)} ${t("cpu")} · ${formatMemory(memory, t)}`;
}

function sessionProcessResourceText(t: (key: string) => string, session: LiveSession): string {
  if ((session.process_count ?? 0) <= 0) return t("unavailable");
  return processResourceText(t, session.process_cpu_percent, session.process_memory_bytes);
}

function processMatchesFilter(process: LiveProcess, filter: ProcessFilter): boolean {
  if (filter.kind === "all") return true;
  if (filter.kind === "runtime") return (process.tool || "unknown") === filter.id;
  if (filter.kind === "host") return processHostKey(process) === filter.id;
  if (filter.id === "direct") return (process.main_sessions ?? 0) > 0;
  if (filter.id === "subagent") return (process.subagent_sessions ?? 0) > 0;
  if (filter.id === "unknown") return (process.unknown_role_sessions ?? 0) > 0;
  return (process.mapped_sessions ?? 0) === 0;
}

function processHostKey(process: LiveProcess): string {
  const host = process.host_app;
  if (!host?.name) return "";
  return host.pid ? `${host.name}:${host.pid}` : host.name;
}

function processIdentity(process: LiveProcess, t: (key: string) => string): string {
  return process.display_name || toolDisplayName(process.tool) || process.command?.split(/\s+/)[0] || t("process");
}

function isSameProcessFilter(a: ProcessFilter, b: ProcessFilter): boolean {
  return a.kind === b.kind && a.id === b.id;
}

function processFilterLabel(t: (key: string) => string, filter: ProcessFilter): string {
  if (filter.kind === "all") return t("allProcesses");
  if (filter.kind === "runtime") return `${t("runtimeField")} · ${toolDisplayName(filter.id)}`;
  if (filter.kind === "host") return `${t("host")} · ${filter.id}`;
  return `${t("role")} · ${processRoleFilterName(t, filter.id)}`;
}

function processRoleFilterName(t: (key: string) => string, id: ProcessFilter["id"]): string {
  switch (id) {
    case "direct":
      return t("main");
    case "subagent":
      return t("subagent");
    case "unknown":
      return t("unknown");
    case "unmapped":
      return t("unmatched");
    default:
      return t("all");
  }
}

function processSummaryMix(t: (key: string) => string, direct = 0, subagent = 0, unknown = 0, unmapped = 0): string {
  return `${t("mainShort")} ${direct} · ${t("subagentShort")} ${subagent} · ${t("unknown")} ${unknown} · ${t("unmatched")} ${unmapped}`;
}

function ToolMix({ t, snapshot }: { t: (key: string) => string; snapshot: Snapshot }) {
  const tools = Object.entries(snapshot.current_by_tool ?? {}).sort((a, b) => (b[1].session_concurrency ?? 0) - (a[1].session_concurrency ?? 0));
  return (
    <div className="tool-mix">
      {tools.length ? tools.slice(0, 4).map(([tool, metrics]) => (
        <span className="tool-mix-item" key={tool}>
          <ToolIcon t={t} tool={tool} />
          <strong>{toolDisplayName(tool)}</strong>
          <em>{metrics.active_burst_concurrency ?? 0}/{metrics.session_concurrency ?? 0}</em>
        </span>
      )) : <span className="muted-inline">{t("unavailable")}</span>}
    </div>
  );
}

function LedgerNavigation({
  t,
  activeTab,
  setActiveTab,
  query,
  setQuery,
  items,
  selection,
  setSelection,
}: {
  t: (key: string) => string;
  activeTab: RailTab;
  setActiveTab: (tab: RailTab) => void;
  query: string;
  setQuery: (value: string) => void;
  items: RailItem[];
  selection: Selection;
  setSelection: (value: Selection) => void;
}) {
  return (
    <section className="ledger-nav">
      <div className="ledger-nav-head">
        <div className="rail-tabs" role="tablist">
          {(["projects", "sessions", "processes"] as RailTab[]).map((tab) => (
            <button key={tab} className={`rail-tab ${activeTab === tab ? "is-active" : ""}`} type="button" role="tab" data-focus-key={focusKey("ledger-nav-tab", tab)} onClick={() => setActiveTab(tab)}>
              {tab === "projects" ? <GitBranch size={14} /> : tab === "sessions" ? <Bot size={14} /> : <Server size={14} />}
              {t(tab)}
            </button>
          ))}
        </div>
        <div className="rail-search">
          <Search size={15} />
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("search")} autoComplete="off" spellCheck={false} />
        </div>
      </div>
      <div className="ledger-nav-list">
        {items.slice(0, 8).map((item) => (
          <button
            className={`ledger-chip ${selection.id === item.id && selection.type === item.type ? "is-selected" : ""}`}
            type="button"
            key={`${item.type}-${item.id}`}
            data-focus-key={focusKey("ledger-nav-item", item.type, item.id)}
            onClick={() => setSelection({ type: item.type, id: item.id } as Selection)}
          >
            <span>{item.title}</span>
            <em>{item.value}</em>
          </button>
        ))}
      </div>
    </section>
  );
}

function Topbar({
  t,
  lang,
  setLang,
  theme,
  setTheme,
  compact,
  running,
  error,
  refreshSnapshot,
  refreshInterval,
  cycleRefreshInterval,
}: {
  t: (key: string) => string;
  lang: Lang;
  setLang: (lang: Lang) => void;
  theme: Theme;
  setTheme: (theme: Theme) => void;
  compact: boolean;
  running: boolean;
  error: string | null;
  refreshSnapshot: () => void;
  refreshInterval: number;
  cycleRefreshInterval: () => void;
}) {
  const topbarStatusTone = error ? "bad" : running ? "running" : "idle";
  const showTopbarStatus = !!error || running;
  return (
    <header className="topbar">
      <div className="brand">
        <span className="brand-mark" aria-hidden="true">
          <svg viewBox="0 0 32 32" focusable="false">
            <rect className="mark-plate" x="3" y="3" width="26" height="26" rx="8" />
            <path className="mark-orbit" d="M8 20.5C10.3 16 13.2 13.8 16.6 13.8C20.3 13.8 22.4 10.9 24.6 7.8" />
            <rect className="mark-bar a" x="8" y="18" width="4.4" height="7" rx="2.2" />
            <rect className="mark-bar b" x="14" y="13" width="4.4" height="12" rx="2.2" />
            <rect className="mark-bar c" x="20" y="9" width="4.4" height="16" rx="2.2" />
            <circle className="mark-node" cx="24" cy="8" r="2.6" />
          </svg>
        </span>
        <div className="brand-text">
          <span className="brand-name">{BRAND_NAME}</span>
          <span className="brand-sub">{t("sub")}</span>
        </div>
        <div className="brand-actions">
          {compact ? null : <Pill tone="safe">{t("loopback")}</Pill>}
          <button className={`icon-btn topbar-refresh-action ${running ? "is-refreshing" : ""}`} type="button" data-focus-key={focusKey("topbar-refresh")} onClick={refreshSnapshot} title={running ? t("running") : t("refresh")} aria-label={running ? t("running") : t("refresh")} aria-busy={running}>
            <RefreshCw size={16} className={running ? "spin" : ""} />
          </button>
          {showTopbarStatus ? <Pill tone={topbarStatusTone}>{error ? t("failed") : running ? t("running") : t("idle")}</Pill> : null}
        </div>
      </div>
      <div className="topbar-meta">
        {!compact ? (
          <button className="kbd-hint" type="button" data-focus-key={focusKey("topbar-refresh-interval")} onClick={cycleRefreshInterval} title={t("autoRefresh")}>
            <kbd>{formatRefreshInterval(refreshInterval, t)}</kbd> {t("auto")}
          </button>
        ) : null}
        <LanguageControl t={t} lang={lang} setLang={setLang} />
        <button className="icon-btn" type="button" data-focus-key={focusKey("topbar-theme")} onClick={() => setTheme(theme === "light" ? "dark" : "light")} title={t("toggleTheme")} aria-label={t("toggleTheme")}>
          {theme === "light" ? <Moon size={16} /> : <Sun size={16} />}
        </button>
        {compact ? (
          <>
            <button className="icon-btn" type="button" data-focus-key={focusKey("topbar-dashboard")} onClick={() => postHostAction("open_dashboard")} title={t("dashboard")} aria-label={t("dashboard")}>
              <ArrowUpRight size={16} />
            </button>
            <button className="icon-btn" type="button" data-focus-key={focusKey("topbar-close")} onClick={() => postHostAction("close")} title={t("close")} aria-label={t("close")}>
              <X size={16} />
            </button>
          </>
        ) : null}
      </div>
    </header>
  );
}

function Rail({
  t,
  activeTab,
  setActiveTab,
  query,
  setQuery,
  items,
  selection,
  setSelection,
  compact,
}: {
  t: (key: string) => string;
  activeTab: RailTab;
  setActiveTab: (tab: RailTab) => void;
  query: string;
  setQuery: (value: string) => void;
  items: RailItem[];
  selection: Selection;
  setSelection: (value: Selection) => void;
  compact: boolean;
}) {
  const tabs: Array<{ id: RailTab; icon: React.ReactNode }> = [
    { id: "projects", icon: <GitBranch size={14} /> },
    { id: "sessions", icon: <Bot size={14} /> },
    { id: "processes", icon: <Server size={14} /> },
  ];
  return (
    <aside className="rail">
      <div className="rail-tabs" role="tablist">
        {tabs.map((tab) => (
          <button key={tab.id} className={`rail-tab ${activeTab === tab.id ? "is-active" : ""}`} type="button" role="tab" data-focus-key={focusKey("rail-tab", tab.id)} onClick={() => setActiveTab(tab.id)}>
            {tab.icon}
            {t(tab.id)}
          </button>
        ))}
      </div>
      <div className="rail-panel">
        <div className="rail-search">
          <Search size={15} />
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("search")} autoComplete="off" spellCheck={false} />
        </div>
        <div className="rail-list" role="listbox" aria-label={t(activeTab)}>
          <button className={`act ${selection.type === "overview" ? "is-selected" : ""}`} data-kind="scan" type="button" data-focus-key={focusKey("rail-item", "overview")} onClick={() => setSelection({ type: "overview", id: "overview" })}>
            <div className="act-top">
              <span className="act-title">{t("overview")}</span>
              <span className="act-state" />
            </div>
            <div className="act-desc">{t("emptySub")}</div>
            <code className="act-cmd">/api/snapshot</code>
          </button>
          <div className="rail-group-title">
            {t(activeTab)}
            <span className="count">{items.length}</span>
          </div>
          {items.map((item) => (
            <button
              className={`act ${selection.id === item.id && selection.type === item.type ? "is-selected" : ""} ${ACTIVE.has(item.status) ? "is-running" : item.status === "done" ? "is-done" : item.status === "failed" ? "is-failed" : ""}`}
              data-kind={item.kind}
              data-focus-key={focusKey("rail-item", item.type, item.id)}
              key={`${item.type}-${item.id}`}
              type="button"
              onClick={() => setSelection({ type: item.type, id: item.id } as Selection)}
            >
              <div className="act-top">
                <span className="act-title">{item.title}</span>
                <span className="act-state" />
              </div>
              <div className="act-desc">{item.description}</div>
              <code className="act-cmd">{item.command}</code>
              {!compact ? (
                <div className="act-tags">
                  {item.tags.map((tag) => <span className="tag" key={tag}>{tag}</span>)}
                  <span className="tag tag-real">{item.value}</span>
                </div>
              ) : null}
            </button>
          ))}
        </div>
      </div>
    </aside>
  );
}

function Pane({
  t,
  snapshot,
  selected,
  selection,
  setSelection,
  logTab,
  setLogTab,
  refreshSnapshot,
  compact,
}: {
  t: (key: string) => string;
  snapshot: Snapshot | null;
  selected: SelectedView;
  selection: Selection;
  setSelection: (value: Selection) => void;
  logTab: LogTab;
  setLogTab: (value: LogTab) => void;
  refreshSnapshot: () => void;
  compact: boolean;
}) {
  if (!snapshot) {
    return (
      <main className="pane">
        <section className="empty-pane">
          <div className="empty-glyph"><Terminal size={34} /></div>
          <p className="empty-title">{t("noData")}</p>
          <p className="empty-sub">{t("emptySub")}</p>
        </section>
      </main>
    );
  }
  return (
    <main className="pane">
      <section className="result">
        <div className="run-head">
          <div className="run-head-main">
            <span className={`badge badge-${selected.status}`}>{selected.status}</span>
            <h1>{selected.title}</h1>
          </div>
          <div className="run-head-meta">
            <code className="cmd">{selected.command}</code>
            <button className="ghost-btn" type="button" data-focus-key={focusKey("detail-refresh")} onClick={refreshSnapshot}>{t("refresh")}</button>
            {!compact ? <button className="ghost-btn" type="button" data-focus-key={focusKey("detail-overview")} onClick={() => setSelection({ type: "overview", id: "overview" })}>{t("overview")}</button> : null}
          </div>
        </div>
        <Timeline t={t} snapshot={snapshot} selected={selected} />
        <Metrics t={t} snapshot={snapshot} selected={selected} compact={compact} />
        <ProjectWorkspace t={t} snapshot={snapshot} selection={selection} setSelection={setSelection} compact={compact} />
        <div className="logwrap">
          <div className="logtabs">
            {(["summary", "evidence", "trend"] as LogTab[]).map((tab) => (
              <button className={`logtab ${logTab === tab ? "is-active" : ""}`} type="button" key={tab} data-focus-key={focusKey("log-tab", tab)} onClick={() => setLogTab(tab)}>
                {t(tab)}
              </button>
            ))}
            <div className="logtabs-spacer" />
            {selection.type === "overview" ? <span className="find-count">{snapshot.generated_at ? formatDateTime(snapshot.generated_at) : ""}</span> : null}
          </div>
          <div className="logbody">
            <pre className="log">{renderLogText(t, snapshot, selected, logTab)}</pre>
          </div>
        </div>
      </section>
    </main>
  );
}

function Timeline({ t, snapshot, selected }: { t: (key: string) => string; snapshot: Snapshot; selected: SelectedView }) {
  const stats = snapshot.transcript_stats;
  const hasScan = (stats?.scanned_files ?? 0) > 0;
  const mapped = (snapshot.summary?.mapping_coverage_pct ?? 0) > 0;
  const hasHistory = (snapshot.history?.retained_sample_count ?? 0) > 0;
  return (
    <div className="timeline">
      <Step label={t("scan")} state={hasScan ? "done" : "active"} />
      <div className="tl-line" />
      <Step label={t("mapping")} state={mapped ? "done" : selected.status === "failed" ? "failed" : "active"} />
      <div className="tl-line" />
      <Step label={t("history")} state={hasHistory ? "done" : "empty"} />
      <div className="tl-spacer" />
      <div className="tl-time">{snapshot.generated_at ? formatDateTime(snapshot.generated_at) : "0ms"}</div>
    </div>
  );
}

function Step({ label, state }: { label: string; state: "done" | "active" | "failed" | "empty" }) {
  return (
    <div className={`tl-step ${state === "empty" ? "" : `is-${state}`}`}>
      <span className="tl-dot" />
      <span className="tl-label">{label}</span>
    </div>
  );
}

function Metrics({ t, snapshot, selected, compact }: { t: (key: string) => string; snapshot: Snapshot; selected: SelectedView; compact: boolean }) {
  const current = snapshot.current ?? {};
  const summary = snapshot.summary ?? {};
  const items = selectionMetrics(t, snapshot, selected);
  const base = [
    { key: t("metricFresh"), value: current.active_burst_concurrency ?? 0, cls: "is-accent", icon: <Activity size={15} /> },
    { key: t("metricSessions"), value: current.session_concurrency ?? 0, cls: "", icon: <Bot size={15} /> },
    { key: t("metricProcesses"), value: current.pid_concurrency ?? 0, cls: "", icon: <Server size={15} /> },
    { key: t("metricMatched"), value: formatPct(summary.mapping_coverage_pct), cls: "is-ok", icon: <Gauge size={15} /> },
  ];
  return (
    <div className="metrics">
      {(compact ? items.concat(base).slice(0, 4) : items.concat(base).slice(0, 8)).map((metric) => (
        <div className={`metric ${metric.cls}`} key={metric.key}>
          <b>{metric.icon}<span>{metric.key}</span></b>
          <span>{metric.value}</span>
        </div>
      ))}
    </div>
  );
}

function ProjectWorkspace({
  t,
  snapshot,
  selection,
  setSelection,
  compact,
}: {
  t: (key: string) => string;
  snapshot: Snapshot;
  selection: Selection;
  setSelection: (value: Selection) => void;
  compact: boolean;
}) {
  const projects = useMemo(() => orderedProjects(snapshot), [snapshot]);
  const selectedProject = selection.type === "project" ? projects.find((project) => safeID(project.project) === selection.id) : undefined;
  const selectedSession = selection.type === "session" ? (snapshot.live_sessions ?? []).find((session) => safeID(session.session_id) === selection.id) : undefined;
  const selectedProcess = selection.type === "process" ? (snapshot.live_processes ?? []).find((process) => String(process.pid ?? "") === selection.id) : undefined;
  const { openProjects, openProject, toggleProject } = useProjectDisclosure(projects, compact ? 0 : 2);
  const selectedProjectId = selectedProject ? safeID(selectedProject.project) : "";
  const lastSelectedProjectRef = useRef("");

  useEffect(() => {
    if (selectedProjectId && lastSelectedProjectRef.current !== selectedProjectId) {
      openProject(selectedProjectId);
    }
    lastSelectedProjectRef.current = selectedProjectId;
  }, [openProject, selectedProjectId]);

  if (selection.type === "session" && selectedSession) {
    return <SessionEvidencePanel t={t} session={selectedSession} />;
  }
  if (selection.type === "process" && selectedProcess) {
    return <ProcessEvidencePanel t={t} process={selectedProcess} />;
  }
  if (selectedProject) {
    const isExpanded = openProjects.has(selectedProjectId);
    return (
      <div className="workspace">
        <ScanBoundary t={t} snapshot={snapshot} compact={compact} />
        <ProjectTreeRow
          t={t}
          snapshot={snapshot}
          project={selectedProject}
          selection={selection}
          setSelection={setSelection}
          compact={compact}
          expanded={isExpanded}
          onToggle={() => toggleProject(selectedProjectId)}
          onOpen={() => openProject(selectedProjectId)}
          rank={1}
        />
      </div>
    );
  }
  return (
    <div className="workspace">
      <ScanBoundary t={t} snapshot={snapshot} compact={compact} />
      <div className="project-tree-list">
        {projects.length ? projects.map((project, index) => {
          const projectId = safeID(project.project);
          return (
            <ProjectTreeRow
              key={projectId}
              t={t}
              snapshot={snapshot}
              project={project}
              selection={selection}
              setSelection={setSelection}
              compact={compact}
              expanded={openProjects.has(projectId)}
              onToggle={() => toggleProject(projectId)}
              onOpen={() => openProject(projectId)}
              rank={index + 1}
            />
          );
        }) : (
          <section className="empty-inline">
            <Layers size={18} />
            <span>{t("noData")}</span>
          </section>
        )}
      </div>
    </div>
  );
}

function ScanBoundary({ t, snapshot, compact }: { t: (key: string) => string; snapshot: Snapshot; compact: boolean }) {
  const stats = snapshot.transcript_stats ?? {};
  const pieces = [
    { label: t("parsed"), value: stats.parsed_files ?? 0 },
    { label: t("candidates"), value: stats.scanned_files ?? 0 },
    { label: t("deferred"), value: deferredScanValue(t, stats) },
    { label: t("tail"), value: stats.tail_parsed_files ?? 0 },
    { label: t("source"), value: stats.cached ? t("cached") : t("fresh") },
    { label: t("scanWindow"), value: formatAge(stats.foreground_scan_lookback_seconds, t) },
  ];
  return (
    <section className="scan-boundary" aria-label={t("scan")}>
      <div className="scan-boundary-main">
        <Activity size={15} />
        <span>{t("scan")}</span>
      </div>
      <div className="scan-boundary-grid">
        {pieces.map((piece) => (
          <span className="scan-readout" key={piece.label} title={`${piece.label}: ${piece.value}`}>
            <b>{piece.label}</b>
            <strong>{piece.value}</strong>
          </span>
        ))}
      </div>
    </section>
  );
}

function ProjectTreeRow({
  t,
  snapshot,
  project,
  selection,
  setSelection,
  compact,
  expanded,
  onToggle,
  onOpen,
  rank,
  setHoverDetail,
}: {
  t: (key: string) => string;
  snapshot: Snapshot;
  project: ProjectSnapshot;
  selection: Selection;
  setSelection: (value: Selection) => void;
  compact: boolean;
  expanded: boolean;
  onToggle?: () => void;
  onOpen?: () => void;
  rank?: number;
  setHoverDetail?: HoverDetailSink;
}) {
  const sessions = sessionsForProject(snapshot, project);
  const counts = projectRoleCounts(project, sessions);
  const projectResources = projectProcessResources(snapshot, sessions);
  const resourceText = processResourceText(t, projectResources.cpu, projectResources.memory);
  const projectId = safeID(project.project);
  const title = project.project || t("unassigned");
  const evidenceItems = projectEvidenceItems(t, project, compact);
  const projectAge = formatAge(project.last_event_age_seconds, t);
  const projectMeta = projectAge;
  const toolSummary = (project.tools ?? []).map((tool) => `${toolDisplayName(tool.tool)} ${tool.active_burst_count ?? 0}/${tool.session_count ?? 0}`).join(" · ") || t("unavailable");
  const projectHoverDetail = `${counts.activeTotal} ${t("active")} / ${counts.total} ${t("sessions")} · ${project.process_count ?? 0} ${t("processes")} · ${resourceText}`;
  const projectHoverMeta = `${t("lastEvent")} ${projectAge} · ${t("tools")}: ${toolSummary}`;
  const projectHoverPayload: HoverDetailPayload = { kind: "project", id: projectId, title, detail: projectHoverDetail, meta: projectHoverMeta };
  const selected = selection.type === "project" && selection.id === projectId;
  const rowClassName = [
    "project-tree-row",
    expanded ? "expanded" : "",
    selected ? "is-selected" : "",
    counts.activeTotal > 0 ? "has-active" : "",
  ].filter(Boolean).join(" ");
  const selectProject = () => {
    if (selected) {
      onToggle?.();
      return;
    }
    setSelection({ type: "project", id: projectId });
    if (!expanded) {
      onOpen?.();
    }
  };
  const disclosureLabel = expanded ? t("collapseDetails") : t("expandDetails");
  const showProjectHover = (event: React.PointerEvent<HTMLElement> | React.FocusEvent<HTMLElement>) => setHoverDetail?.(projectHoverPayload, event);
  const clearProjectHover = () => setHoverDetail?.(null);
  const clearProjectFocusHover = (event: React.FocusEvent<HTMLElement>) => {
    if (event.currentTarget.contains(event.relatedTarget as Node | null)) return;
    clearProjectHover();
  };
  return (
    <article className={rowClassName}>
      <div className="project-tree-head" onPointerEnter={showProjectHover} onPointerMove={showProjectHover} onPointerLeave={clearProjectHover} onFocus={showProjectHover} onBlur={clearProjectFocusHover}>
        <span className="project-rank">{rank ?? "-"}</span>
        <button className="project-disclosure" type="button" onClick={onToggle} aria-expanded={expanded} aria-label={disclosureLabel}>
          <ChevronDown size={15} aria-hidden="true" />
        </button>
        <button className="project-select" type="button" data-focus-key={focusKey("project", projectId)} onClick={selectProject} aria-current={selected ? "true" : undefined} aria-expanded={expanded} aria-label={title}>
          <span>{title}</span>
          <small>{projectMeta}</small>
        </button>
        {compact ? <ProjectCompactMetrics t={t} counts={counts} processCount={project.process_count ?? 0} resourceText={resourceText} /> : <ProjectMetricMatrix t={t} counts={counts} processCount={project.process_count ?? 0} resourceText={resourceText} />}
        <ToolStrip t={t} tools={project.tools ?? []} />
      </div>
      {expanded ? (
        <>
          <div className="project-evidence-strip" aria-label={t("evidence")}>
            {evidenceItems.map((item) => (
              <span className={`project-evidence-chip ${item.tone ?? ""}`} key={item.label}>
                <b>{item.label}</b>
                <em>{item.value}</em>
              </span>
            ))}
          </div>
          <SessionTree t={t} sessions={sessions} selection={selection} setSelection={setSelection} compact={compact} setHoverDetail={setHoverDetail} />
        </>
      ) : null}
      {setHoverDetail ? null : <RowHoverDetail title={title} detail={projectHoverDetail} meta={projectHoverMeta} />}
    </article>
  );
}

function ProjectCompactMetrics({ t, counts, processCount, resourceText }: { t: (key: string) => string; counts: RoleCounts; processCount: number; resourceText: string }) {
  const activeMainTitle = projectMetricCellTitle(t, "active", "main", counts.activeMain);
  const activeSubagentTitle = projectMetricCellTitle(t, "active", "subagent", counts.activeSub);
  const activeTotalTitle = projectMetricCellTitle(t, "active", "total", counts.activeTotal);
  const allMainTitle = projectMetricCellTitle(t, "all", "main", counts.main);
  const allSubagentTitle = projectMetricCellTitle(t, "all", "subagent", counts.sub);
  const allTotalTitle = projectMetricCellTitle(t, "all", "total", counts.total);
  const activeTitle = `${activeTotalTitle} · ${activeMainTitle} · ${activeSubagentTitle}`;
  const allTitle = `${allTotalTitle} · ${allMainTitle} · ${allSubagentTitle}`;
  const processTitle = projectMetricProcessTitle(t, processCount);
  return (
    <div className="project-compact-metrics" aria-label={t("metricSessions")}>
      <span className="project-compact-cluster active" aria-label={activeTitle}>
        <b>{t("activeShort")}</b>
        <strong aria-label={activeTotalTitle}>{counts.activeTotal}</strong>
        <em aria-label={`${activeMainTitle} · ${activeSubagentTitle}`}>{t("mainShort")}{counts.activeMain} · {t("subagentShort")}{counts.activeSub}</em>
      </span>
      <span className="project-compact-cluster all" aria-label={allTitle}>
        <b>{t("allShort")}</b>
        <strong aria-label={allTotalTitle}>{counts.total}</strong>
        <em aria-label={`${allMainTitle} · ${allSubagentTitle}`}>{t("mainShort")}{counts.main} · {t("subagentShort")}{counts.sub}</em>
      </span>
      <span className="project-compact-proc" aria-label={processTitle}>
        <i>{t("processShort")}</i>
        <strong>{processCount}</strong>
        <em>{resourceText}</em>
      </span>
    </div>
  );
}

function ProjectMetricMatrix({ t, counts, processCount, resourceText }: { t: (key: string) => string; counts: RoleCounts; processCount: number; resourceText: string }) {
  const activeTitle = projectMetricGroupTitle(t, "active", counts);
  const allTitle = projectMetricGroupTitle(t, "all", counts);
  const mainTitle = projectMetricObjectTitle(t, "main");
  const subagentTitle = projectMetricObjectTitle(t, "subagent");
  const totalTitle = projectMetricObjectTitle(t, "total");
  const processTitle = projectMetricProcessTitle(t, processCount);
  return (
    <div className="project-matrix" aria-label={t("metricSessions")}>
      <span />
      <b aria-label={mainTitle}>{t("main")}</b>
      <b aria-label={subagentTitle}>{t("subagent")}</b>
      <b aria-label={totalTitle}>{t("total")}</b>
      <b aria-label={activeTitle}>{t("active")}</b>
      <ProjectMetricNumber t={t} scope="active" metric="main" value={counts.activeMain} />
      <ProjectMetricNumber t={t} scope="active" metric="subagent" value={counts.activeSub} />
      <ProjectMetricNumber t={t} scope="active" metric="total" value={counts.activeTotal} />
      <b aria-label={allTitle}>{t("all")}</b>
      <ProjectMetricNumber t={t} scope="all" metric="main" value={counts.main} />
      <ProjectMetricNumber t={t} scope="all" metric="subagent" value={counts.sub} />
      <ProjectMetricNumber t={t} scope="all" metric="total" value={counts.total} />
      <span className="project-proc" aria-label={processTitle}>
        <Server size={12} />
        <strong>{processCount}</strong>
        <em>{resourceText}</em>
      </span>
    </div>
  );
}

function projectProcessResources(snapshot: Snapshot, sessions: LiveSession[]): { cpu: number; memory: number } {
  const sessionKeys = new Set(sessions.map((session) => `${session.tool || ""}\x00${session.session_id || ""}`));
  const sessionIDs = new Set(sessions.map((session) => session.session_id || "").filter(Boolean));
  let cpu = 0;
  let memory = 0;
  for (const process of snapshot.live_processes ?? []) {
    const matched = (process.mapped_session_evidence ?? []).some((evidence) => sessionKeys.has(`${evidence.tool || process.tool || ""}\x00${evidence.session_id || ""}`))
      || (process.session_ids ?? []).some((id) => sessionIDs.has(id));
    if (!matched) continue;
    cpu += process.cpu_percent ?? 0;
    memory += process.memory_bytes ?? 0;
  }
  return { cpu, memory };
}

function ProjectMetricNumber({ t, scope, metric, value }: { t: (key: string) => string; scope: ProjectMetricScope; metric: ProjectMetricObject; value: number }) {
  const title = projectMetricCellTitle(t, scope, metric, value);
  return <strong aria-label={title}>{value}</strong>;
}

function ToolStrip({ t, tools }: { t: (key: string) => string; tools: ProjectTool[] }) {
  if (!tools.length) return null;
  return (
    <div className="tool-strip" aria-label={t("projectToolStripLabel")}>
      {tools.map((tool) => {
        const toolName = tool.tool || "unknown";
        const baseTitle = formatCopy(t("projectToolBadgeTooltip"), {
          tool: toolDisplayName(toolName),
          active: tool.active_burst_count ?? 0,
          sessions: tool.session_count ?? 0,
        });
        const tokenTitle = tool.token_usage && tokenUsageHasValue(tool.token_usage) ? `${t("tokenUsage")}: ${formatTokenUsageSummary(tool.token_usage, t)}` : "";
        const title = tokenTitle ? `${baseTitle} ${tokenTitle}` : baseTitle;
        return (
          <span className="tool-mark" key={toolName} aria-label={title}>
            <ToolIcon t={t} tool={toolName} title={title} />
            <strong>{tool.active_burst_count ?? 0}</strong>
            <small>/{tool.session_count ?? 0}</small>
          </span>
        );
      })}
    </div>
  );
}

function SessionTree({
  t,
  sessions,
  selection,
  setSelection,
  compact,
  setHoverDetail,
}: {
  t: (key: string) => string;
  sessions: LiveSession[];
  selection: Selection;
  setSelection: (value: Selection) => void;
  compact: boolean;
  setHoverDetail?: HoverDetailSink;
}) {
  const groups = buildToolSessionGroups(sessions);
  const groupLimit = compact ? 2 : 4;
  const linkedLimit = compact ? 2 : 3;
  const childLimit = compact ? 3 : 3;
  const unlinkedLimit = compact ? 3 : 4;
  const [showOverflow, setShowOverflow] = useState(false);
  const collapsedGroups = groups.slice(0, groupLimit);
  const hiddenGroups = groups.slice(groupLimit).reduce((total, group) => total + group.sessions.length, 0);
  const hiddenTotal = hiddenGroups + collapsedGroups.reduce((total, group) => total + hiddenToolSessionCount(group, linkedLimit, childLimit, unlinkedLimit), 0);
  const visibleGroups = showOverflow ? groups : collapsedGroups;
  const overflowLabel = countLabel(t, showOverflow ? "lessCount" : "moreCount", hiddenTotal);
  if (!sessions.length) {
    return <div className="session-tree empty">{t("empty")}</div>;
  }
  return (
    <div className="session-tree">
      {visibleGroups.map((group) => {
        const visibleLinked = showOverflow ? group.linked : group.linked.slice(0, linkedLimit);
        const visibleUnlinked = showOverflow ? group.unlinked : group.unlinked.slice(0, unlinkedLimit);
        const visibleUnknown = showOverflow ? group.unknown : group.unknown.slice(0, Math.max(1, unlinkedLimit - visibleUnlinked.length));
        return (
          <section className="session-tool-block" key={group.tool}>
            <div className="session-tool-head">
              <span><ToolIcon t={t} tool={group.tool} />{toolDisplayName(group.tool)}</span>
              <strong>{group.activeCount}/{group.sessions.length}</strong>
            </div>
            {visibleLinked.map((branch) => (
              <section className="session-branch" key={sessionIdentity(branch.parent)}>
                {!compact ? (
                  <div className="session-group-head">
                    <span>{branch.parent.agent_nickname || shortID(branch.parent.session_id) || t("main")}</span>
                    <strong>{branch.children.length}</strong>
                  </div>
                ) : null}
                <SessionLine t={t} session={branch.parent} selection={selection} setSelection={setSelection} compact={compact} setHoverDetail={setHoverDetail} />
                {(showOverflow ? branch.children : branch.children.slice(0, childLimit)).map((session) => (
                  <SessionLine key={sessionIdentity(session)} t={t} session={session} selection={selection} setSelection={setSelection} compact={compact} child setHoverDetail={setHoverDetail} />
                ))}
              </section>
            ))}
            {visibleUnlinked.length ? (
              <section className="session-branch unlinked">
                <div className="session-group-head">
                  <span><GitBranch size={14} />{t("unlinkedSubagents")}</span>
                  <strong>{group.unlinked.length}</strong>
                </div>
                {visibleUnlinked.map((session) => (
                  <SessionLine key={sessionIdentity(session)} t={t} session={session} selection={selection} setSelection={setSelection} compact={compact} setHoverDetail={setHoverDetail} />
                ))}
              </section>
            ) : null}
            {visibleUnknown.length ? (
              <section className="session-branch unknown">
                <div className="session-group-head">
                  <span><GitBranch size={14} />{t("unknown")}</span>
                  <strong>{group.unknown.length}</strong>
                </div>
                {visibleUnknown.map((session) => (
                  <SessionLine key={sessionIdentity(session)} t={t} session={session} selection={selection} setSelection={setSelection} compact={compact} setHoverDetail={setHoverDetail} />
                ))}
              </section>
            ) : null}
          </section>
        );
      })}
      {hiddenTotal ? (
        <button className={`session-tree-more ${showOverflow ? "is-expanded" : ""}`} type="button" onClick={() => setShowOverflow((value) => !value)} aria-expanded={showOverflow} aria-label={overflowLabel} title={overflowLabel}>
          <ChevronDown size={12} aria-hidden="true" />
          <span>{overflowLabel}</span>
        </button>
      ) : null}
    </div>
  );
}

function SessionLine({
  t,
  session,
  selection,
  setSelection,
  compact,
  child = false,
  setHoverDetail,
}: {
  t: (key: string) => string;
  session: LiveSession;
  selection: Selection;
  setSelection: (value: Selection) => void;
  compact: boolean;
  child?: boolean;
  setHoverDetail?: HoverDetailSink;
}) {
  const role = normalizedRole(session.session_role);
  const sid = session.session_id || "";
  const host = session.host_apps?.[0];
  const evidenceItems = sessionEvidenceItems(t, session, compact);
  const processText = compact ? `${session.process_count ?? 0}p` : `${session.process_count ?? 0} ${t("pid")}`;
  const resourceText = sessionProcessResourceText(t, session);
  const selected = selection.type === "session" && safeID(sid) === selection.id;
  const title = session.agent_nickname || shortID(sid) || "session";
  const meta = `${formatAge(session.last_event_age_seconds, t)} · ${processText} · ${resourceText} · ${confidenceLabel(t, session.confidence)}`;
  const hostName = host?.name || t("hostUnknown");
  const sessionHoverTitle = `${title} · ${roleLabel(t, role)}`;
  const sessionHoverMeta = `${t("mappingMethod")}: ${mappingMethodLabel(t, session.mapping_method)} · ${t("freshness")}: ${freshnessLabel(t, session.freshness || (session.active_burst ? "active" : "idle"))}`;
  const sessionHoverMetrics = [
    { label: t("tool"), value: toolDisplayName(session.tool) },
    { label: t("host"), value: hostName },
    { label: t("age"), value: formatAge(session.last_event_age_seconds, t) },
    { label: t("processCount"), value: processText },
    { label: t("processCPU"), value: formatCPU(session.process_cpu_percent) },
    { label: t("processMemory"), value: formatMemory(session.process_memory_bytes, t) },
  ];
  const tokenParts = sessionTokenUsageParts(t, session.token_usage);
  const sessionHoverPayload: HoverDetailPayload = { kind: "session", id: safeID(sid), title: sessionHoverTitle, metrics: sessionHoverMetrics, tokenParts, meta: sessionHoverMeta };
  const showSessionHover = (event: React.PointerEvent<HTMLElement> | React.FocusEvent<HTMLElement>) => setHoverDetail?.(sessionHoverPayload, event);
  const clearSessionHover = () => setHoverDetail?.(null);
  const clearSessionFocusHover = (event: React.FocusEvent<HTMLElement>) => {
    if (event.currentTarget.contains(event.relatedTarget as Node | null)) return;
    clearSessionHover();
  };
  const visibleEvidenceItems = (session.process_count ?? 0) > 0
    ? [{ label: t("resources"), value: resourceText, tone: "resource" }, ...evidenceItems]
    : evidenceItems;
  if (compact) {
    return (
      <div className={`session-line role-${role} ${session.active_burst ? "is-active" : ""} ${selected ? "is-selected" : ""} ${child ? "is-child" : ""}`} onPointerEnter={showSessionHover} onPointerMove={showSessionHover} onPointerLeave={clearSessionHover} onFocus={showSessionHover} onBlur={clearSessionFocusHover}>
        <span className="session-role-slot">
          <RoleGlyph t={t} role={role} />
        </span>
        <span className="session-tool-pair">
          <ToolIcon t={t} tool={session.tool || "unknown"} />
          {host ? <HostAppButton t={t} host={host} /> : <HostAppEmpty t={t} />}
        </span>
        <span className="session-title">
          <SessionIdControl t={t} sid={sid} title={title} selected={selected} setSelection={setSelection} />
          <button className="session-meta-button" type="button" data-focus-key={focusKey("session-meta", sid || title)} onClick={() => setSelection({ type: "session", id: safeID(sid) })}>
            <small>{meta}</small>
          </button>
        </span>
        <div className="session-evidence-strip" aria-label={t("evidence")}>
          {visibleEvidenceItems.map((item) => (
            <span className={`session-evidence-chip ${item.tone ?? ""}`} key={item.label}>
              <b>{item.label}</b>
              <em>{item.value}</em>
            </span>
          ))}
        </div>
        {setHoverDetail ? null : <SessionHoverDetail t={t} title={sessionHoverTitle} metrics={sessionHoverMetrics} tokenParts={tokenParts} meta={sessionHoverMeta} />}
      </div>
    );
  }
  return (
    <div className={`session-line role-${role} ${session.active_burst ? "is-active" : ""} ${selected ? "is-selected" : ""} ${child ? "is-child" : ""}`} onPointerEnter={showSessionHover} onPointerMove={showSessionHover} onPointerLeave={clearSessionHover} onFocus={showSessionHover} onBlur={clearSessionFocusHover}>
      <span className="session-main">
        <RoleGlyph t={t} role={role} />
        <span className="session-title">
          <SessionIdControl t={t} sid={sid} title={title} selected={selected} setSelection={setSelection} />
          <button className="session-meta-button" type="button" data-focus-key={focusKey("session-meta", sid || title)} onClick={() => setSelection({ type: "session", id: safeID(sid) })}>
            <small>{meta}</small>
          </button>
        </span>
      </span>
      <span className="session-tool-pair">
        <ToolIcon t={t} tool={session.tool || "unknown"} />
        {host ? <HostAppButton t={t} host={host} /> : <HostAppEmpty t={t} label={!compact} />}
      </span>
      <div className="session-evidence-strip" aria-label={t("evidence")}>
        {evidenceItems.map((item) => (
          <span className={`session-evidence-chip ${item.tone ?? ""}`} key={item.label}>
            <b>{item.label}</b>
            <em>{item.value}</em>
          </span>
        ))}
      </div>
      {setHoverDetail ? null : <SessionHoverDetail t={t} title={sessionHoverTitle} metrics={sessionHoverMetrics} tokenParts={tokenParts} meta={sessionHoverMeta} />}
    </div>
  );
}

function SessionIdControl({
  t,
  sid,
  title,
  selected,
  setSelection,
}: {
  t: (key: string) => string;
  sid: string;
  title: string;
  selected: boolean;
  setSelection: (value: Selection) => void;
}) {
  return (
    <span className="session-id-control" aria-label={sid || title}>
      <button className="session-id-button" type="button" data-focus-key={focusKey("session", sid || title)} aria-current={selected ? "true" : undefined} onClick={() => setSelection({ type: "session", id: safeID(sid) })}>
        <strong>{title}</strong>
      </button>
      <button
        className="session-copy-inline"
        type="button"
        aria-label={t("copySession")}
        data-focus-key={focusKey("session-copy", sid || title)}
        disabled={!sid}
        onClick={(event) => {
          event.stopPropagation();
          copyText(sid);
        }}
      >
        <Copy size={10} />
      </button>
    </span>
  );
}

function SessionEvidencePanel({ t, session }: { t: (key: string) => string; session: LiveSession }) {
  return (
    <section className="entity-panel">
      <div className="entity-title">
        <Bot size={17} />
        <strong>{session.project || t("unassigned")}</strong>
        <span>{roleLabel(t, normalizedRole(session.session_role))}</span>
      </div>
      <div className="entity-grid">
        <Readout label={t("sessionID")} value={session.session_id || t("unavailable")} />
        <Readout label={t("role")} value={roleLabel(t, normalizedRole(session.session_role))} />
        <Readout label={t("roleConfidence")} value={confidenceLabel(t, session.role_confidence)} />
        <Readout label={t("mappingMethod")} value={mappingMethodLabel(t, session.mapping_method)} />
        <Readout label={t("threadSource")} value={threadSourceLabel(t, session.thread_source)} />
        <Readout label={t("parentThread")} value={session.parent_thread_id || t("unavailable")} />
        <Readout label={t("roleHint")} value={roleHintLabel(t, session.role_hint_source) || agentRoleLabel(t, session.agent_role) || session.agent_nickname || t("unavailable")} />
        <Readout label={t("freshness")} value={freshnessLabel(t, session.freshness || (session.active_burst ? "active" : "idle"))} />
        <Readout label={t("observedDuration")} value={formatAge(session.observed_duration_seconds, t)} />
        <Readout label={t("activeDuration")} value={formatAge(session.active_duration_seconds, t)} />
        <Readout label={t("idleDuration")} value={formatAge(session.idle_duration_seconds, t)} />
        <Readout label={t("resources")} value={sessionProcessResourceText(t, session)} />
        <Readout label={t("tokenUsage")} value={formatTokenUsageSummary(session.token_usage, t)} />
        <Readout label={t("tools")} value={toolDisplayName(session.tool)} />
        <Readout label={t("host")} value={(session.host_apps ?? []).map((app) => app.name).join(", ") || t("unavailable")} />
        <Readout label={t("command")} value={session.path || t("unavailable")} />
      </div>
    </section>
  );
}

function ProcessEvidencePanel({ t, process }: { t: (key: string) => string; process: LiveProcess }) {
  return (
    <section className="entity-panel">
      <div className="entity-title">
        <Server size={17} />
        <strong>{processIdentity(process, t)}</strong>
        <span>{t("pid")} {process.pid ?? t("unavailable")}</span>
      </div>
      <div className="entity-grid">
        <Readout label={t("resources")} value={processResourceText(t, process.cpu_percent, process.memory_bytes)} />
        <Readout label={t("runtimeDuration")} value={process.elapsed || t("unavailable")} />
        <Readout label={t("metricMatched")} value={String(process.mapped_sessions ?? 0)} />
        <Readout label={t("active")} value={String(process.mapped_active_sessions ?? 0)} />
        <Readout label={t("roleMix")} value={`${t("mainShort")} ${process.main_sessions ?? 0} · ${t("subagentShort")} ${process.subagent_sessions ?? 0} · ${t("unknown")} ${process.unknown_role_sessions ?? 0}`} />
        <Readout label={t("mappingMethod")} value={(process.match_methods ?? []).map((method) => mappingMethodLabel(t, method)).join(", ") || t("unavailable")} />
        <Readout label={t("sessions")} value={sessionIDsText(t, process.session_ids)} />
        <Readout label={t("tools")} value={toolDisplayName(process.tool)} />
        <Readout label={t("host")} value={process.host_app?.name || t("unavailable")} />
        <Readout label={t("command")} value={process.command || t("unavailable")} />
      </div>
    </section>
  );
}

function Readout({ label, value }: { label: string; value?: string }) {
  return (
    <span className="readout">
      <b>{label}</b>
      <strong>{value || ""}</strong>
    </span>
  );
}

function ToolIcon({ t, tool, title }: { t: (key: string) => string; tool?: string; title?: string }) {
  const iconName = toolIconName(tool);
  const label = title || formatCopy(t("codingAgentTooltip"), { tool: toolDisplayName(tool) });
  if (!iconName) return <span className="tool-fallback" aria-label={label}>{toolBadgeLabel(tool)}</span>;
  return (
    <span className="tool-icon" aria-label={label}>
      <img src={`/api/tool-icon/${encodeURIComponent(iconName)}`} alt="" loading="lazy" decoding="async" />
    </span>
  );
}

function HostAppButton({ t, host }: { t: (key: string) => string; host: HostApp }) {
  const title = hostAppTitle(t, host);
  return (
    <button className="host-app" type="button" data-focus-key={focusKey("host-app", host.pid ?? host.bundle_path ?? host.name ?? "")} aria-label={title} onClick={() => openHostApp(host)}>
      <span className="host-icon">
        <img src={`/api/host-app-icon/${encodeURIComponent(String(host.pid ?? ""))}`} alt="" loading="lazy" decoding="async" />
      </span>
      <ExternalLink size={12} />
    </button>
  );
}

function HostAppEmpty({ t, label = false }: { t: (key: string) => string; label?: boolean }) {
  const title = t("hostAppUnknown");
  return <span className="host-empty" aria-label={title}>{label ? t("host") : ""}</span>;
}

function hostAppTitle(t: (key: string) => string, host: HostApp): string {
  const app = host.name || String(host.pid ?? t("unavailable"));
  return formatCopy(t(host.pid ? "hostAppOpenTooltip" : "hostAppObservedTooltip"), { app });
}

function projectMetricGroupTitle(t: (key: string) => string, scope: ProjectMetricScope, counts: RoleCounts): string {
  const active = scope === "active";
  return formatCopy(t("projectMetricGroupTooltip"), {
    group: projectMetricScopeLabel(t, scope),
    main: active ? counts.activeMain : counts.main,
    subagent: active ? counts.activeSub : counts.sub,
    sessions: active ? counts.activeTotal : counts.total,
    detail: projectMetricScopeHelp(t, scope),
  });
}

function projectMetricCellTitle(t: (key: string) => string, scope: ProjectMetricScope, metric: ProjectMetricObject, value: number): string {
  return formatCopy(t("projectMetricCellTooltip"), {
    scope: projectMetricScopeLabel(t, scope),
    metric: projectMetricObjectLabel(t, metric),
    value,
    detail: `${projectMetricScopeHelp(t, scope)} ${projectMetricObjectHelp(t, metric)}`,
  });
}

function projectMetricProcessTitle(t: (key: string) => string, value: number): string {
  return formatCopy(t("projectMetricProcessTooltip"), {
    value,
    detail: t("projectMetricProcessHelp"),
  });
}

function projectMetricObjectTitle(t: (key: string) => string, metric: ProjectMetricObject): string {
  return `${projectMetricObjectLabel(t, metric)}: ${projectMetricObjectHelp(t, metric)}`;
}

function projectMetricScopeLabel(t: (key: string) => string, scope: ProjectMetricScope): string {
  return scope === "active" ? t("active") : t("all");
}

function projectMetricScopeHelp(t: (key: string) => string, scope: ProjectMetricScope): string {
  return t(scope === "active" ? "projectMetricActiveGroupHelp" : "projectMetricAllGroupHelp");
}

function projectMetricObjectLabel(t: (key: string) => string, metric: ProjectMetricObject): string {
  if (metric === "main") return t("main");
  if (metric === "subagent") return t("subagent");
  return t("total");
}

function projectMetricObjectHelp(t: (key: string) => string, metric: ProjectMetricObject): string {
  if (metric === "main") return t("projectMetricMainHelp");
  if (metric === "subagent") return t("projectMetricSubagentHelp");
  return t("projectMetricTotalHelp");
}

function LanguageControl({ t, lang, setLang }: { t: (key: string) => string; lang: Lang; setLang: (lang: Lang) => void }) {
  return (
    <div className="lang-control" aria-label={t("language")}>
      <Languages size={14} />
      {(["en", "zh", "ja"] as Lang[]).map((item) => {
        const label = languageDisplayName(item);
        return (
          <button
            key={item}
            className={lang === item ? "is-active" : ""}
            type="button"
            aria-pressed={lang === item}
            aria-label={t("languageOption").replace("{label}", label)}
            data-focus-key={focusKey("language", item)}
            onClick={() => setLang(item)}
          >
            {item.toUpperCase()}
          </button>
        );
      })}
    </div>
  );
}

function TermLabel({ label, tip }: { label: string; tip: string }) {
  return (
    <span className="term-label" tabIndex={0} role="button" data-focus-key={focusKey("term", label)} aria-label={`${label}: ${tip}`} data-tip={tip} title={tip}>
      {label}
    </span>
  );
}

function RowHoverDetail({ title, detail, meta }: { title: string; detail: string; meta?: string }) {
  return (
    <span className="row-hover-detail" aria-hidden="true">
      <strong>{title}</strong>
      <em>{detail}</em>
      {meta ? <small>{meta}</small> : null}
    </span>
  );
}

function SessionHoverDetail({
  t,
  title,
  metrics,
  tokenParts,
  meta,
}: {
  t: (key: string) => string;
  title: string;
  metrics: Array<{ label: string; value: string }>;
  tokenParts: Array<{ label: string; value: string }>;
  meta: string;
}) {
  return (
    <span className="row-hover-detail session-hover-detail" aria-hidden="true">
      <SessionHoverContent t={t} title={title} metrics={metrics} tokenParts={tokenParts} meta={meta} />
    </span>
  );
}

function SessionHoverContent({
  t,
  title,
  metrics,
  tokenParts,
  meta,
}: {
  t: (key: string) => string;
  title: string;
  metrics: Array<{ label: string; value: string }>;
  tokenParts: Array<{ label: string; value: string }>;
  meta: string;
}) {
  return (
    <>
      <strong>{title}</strong>
      <span className="session-hover-grid">
        {metrics.map((item) => (
          <span key={item.label}>
            <b>{item.label}</b>
            <em>{item.value}</em>
          </span>
        ))}
      </span>
      <span className="session-hover-tokens">
        <b>{t("tokenUsage")}</b>
        <span>
          {tokenParts.map((item) => (
            <em key={item.label}><i>{item.label}</i>{item.value}</em>
          ))}
        </span>
      </span>
      <small>{meta}</small>
    </>
  );
}

function sessionTokenUsageParts(t: (key: string) => string, usage?: TokenUsage): Array<{ label: string; value: string }> {
  if (!usage || !tokenUsageHasValue(usage)) return [{ label: t("tokenTotal"), value: t("unavailable") }];
  const cache = (usage.cache_creation_input_tokens ?? 0) + (usage.cache_read_input_tokens ?? 0);
  const parts = [
    { label: t("tokenTotal"), value: formatTokenCount(usage.total_tokens ?? tokenUsageDerivedTotalForHover(usage)) },
    { label: t("tokenInput"), value: formatTokenCount(usage.input_tokens) },
    { label: t("tokenOutput"), value: formatTokenCount(usage.output_tokens) },
  ];
  if (cache > 0) parts.push({ label: t("tokenCache"), value: formatTokenCount(cache) });
  if ((usage.reasoning_output_tokens ?? 0) > 0) parts.push({ label: t("tokenReasoning"), value: formatTokenCount(usage.reasoning_output_tokens) });
  return parts;
}

function tokenUsageDerivedTotalForHover(usage: TokenUsage): number {
  return (usage.input_tokens ?? 0) + (usage.output_tokens ?? 0) + (usage.cache_creation_input_tokens ?? 0) + (usage.cache_read_input_tokens ?? 0);
}

function Pill({ tone, children }: { tone: "safe" | "idle" | "running" | "bad"; children: React.ReactNode }) {
  return (
    <span className={`pill pill-${tone}`}>
      <span className="dot" />
      {children}
    </span>
  );
}

function selectionMetrics(t: (key: string) => string, snapshot: Snapshot, selected: SelectedView) {
  const stats = snapshot.transcript_stats;
  const risk = snapshot.coordination_risk;
  return [
    { key: t("resultKind"), value: selected.kind, cls: "is-accent", icon: <Terminal size={15} /> },
    { key: t("source"), value: stats?.cached ? t("cached") : t("fresh"), cls: "", icon: <Activity size={15} /> },
    { key: t("samples"), value: snapshot.history?.retained_sample_count ?? 0, cls: "", icon: <Gauge size={15} /> },
    { key: t("topProject"), value: risk?.top_project || t("none"), cls: "", icon: <GitBranch size={15} /> },
  ];
}

function RoleGlyph({ t, role }: { t: (key: string) => string; role: "main" | "subagent" | "unknown" }) {
  const label = roleLabel(t, role);
  const icon = role === "main" ? <Terminal size={12} /> : role === "subagent" ? <Bot size={12} /> : <GitBranch size={12} />;
  return (
    <span className="role-glyph" aria-label={label}>
      {icon}
    </span>
  );
}

async function openHostApp(host: HostApp) {
  if (!host.pid) return;
  await fetch(`/api/open-host-app/${encodeURIComponent(String(host.pid))}`, { method: "POST" }).catch(() => undefined);
}

function copyText(value: string) {
  if (!value) return;
  void navigator.clipboard?.writeText(value).catch(() => undefined);
}

function postHostAction(action: "open_dashboard" | "close" | "quit") {
  if (window.webkit?.messageHandlers?.agentLoadAction) {
    window.webkit.messageHandlers.agentLoadAction.postMessage({ action });
    return;
  }
  if (action === "open_dashboard") window.open("/dashboard", "_blank", "noopener,noreferrer");
  if (action === "close") window.close();
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

function initialLang(): Lang {
  const requested = normalizeLang(new URLSearchParams(window.location.search).get("lang"));
  if (requested) return requested;
  const stored = normalizeLang(window.localStorage.getItem("agentload.lang"));
  if (stored) return stored;
  return normalizeLang(window.navigator.language) ?? "en";
}

function normalizeLang(value?: string | null): Lang | null {
  const key = String(value || "").trim().toLowerCase();
  if (key === "zh" || key === "zh-cn" || key === "zh-hans") return "zh";
  if (key === "ja" || key === "ja-jp") return "ja";
  if (key === "en" || key === "en-us" || key === "en-gb") return "en";
  return null;
}

function htmlLang(lang: Lang): string {
  if (lang === "zh") return "zh-CN";
  if (lang === "ja") return "ja";
  return "en";
}

function languageDisplayName(lang: Lang): string {
  if (lang === "zh") return "中文";
  if (lang === "ja") return "日本語";
  return "English";
}

function initialTheme(): Theme {
  return window.localStorage.getItem("agentload.theme") === "light" ? "light" : "dark";
}

function initialRefreshInterval(): number {
  const stored = window.localStorage.getItem(REFRESH_INTERVAL_STORAGE_KEY);
  if (stored === null) return DEFAULT_REFRESH_INTERVAL_MS;
  const raw = Number(stored);
  return REFRESH_INTERVALS_MS.includes(raw as (typeof REFRESH_INTERVALS_MS)[number]) ? raw : DEFAULT_REFRESH_INTERVAL_MS;
}

function surfaceVisible(view: "popover" | "dashboard", popoverVisible: boolean): boolean {
  if (document.visibilityState && document.visibilityState !== "visible") return false;
  return view !== "popover" || popoverVisible;
}

function effectiveAutoRefreshDelay(
  view: "popover" | "dashboard",
  popoverView: PopoverView,
  refreshInterval: number,
  popoverVisible: boolean,
  shell: HTMLElement | null,
  readerActiveUntil: number,
): number {
  if (!refreshInterval || !surfaceVisible(view, popoverVisible)) return 0;
  return readerContextActive(view, popoverView, shell, readerActiveUntil) ? Math.max(refreshInterval, READER_REFRESH_FLOOR_MS) : refreshInterval;
}

function readerContextActive(view: "popover" | "dashboard", popoverView: PopoverView, shell: HTMLElement | null, readerActiveUntil: number): boolean {
  if (readerActiveUntil > Date.now()) return true;
  if (document.scrollingElement && document.scrollingElement.scrollTop > 8) return true;
  const root = shell ?? document;
  if (root.querySelector('[aria-expanded="true"]')) return true;
  if (view === "popover") {
    if (popoverView === "trend") return true;
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
    if (active && document.activeElement !== active) {
      active.focus({ preventScroll: true });
    }
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

function focusKey(...parts: Array<string | number | boolean | null | undefined>): string {
  return parts.map((part) => encodeURIComponent(String(part ?? ""))).join(":");
}

function cssEscape(value: string): string {
  if (window.CSS?.escape) return window.CSS.escape(value);
  return value.replace(/["\\#.;:[\],>+~*^$|=()\s]/g, "\\$&");
}

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
