import React, { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { Activity, ArrowUpRight, Bot, ChevronDown, Copy, ExternalLink, Gauge, GitBranch, Info, Languages, Layers, Moon, RefreshCw, Search, Server, Sun, Terminal, X } from "lucide-react";
import { copy, type Lang } from "./i18n";
import "./styles.css";

const BRAND_NAME = "Agent Load";
const ACTIVE = new Set(["active", "running", "queued"]);
const DEFAULT_REFRESH_INTERVAL_MS = 300_000;
const MANUAL_REFRESH_SETTLE_MS = 450;
const READER_CONTEXT_TTL_MS = 20_000;
const READER_REFRESH_FLOOR_MS = 60_000;
const REFRESH_INTERVALS_MS = [30_000, 60_000, 120_000, 300_000, 0] as const;
const REFRESH_INTERVAL_STORAGE_KEY = "agentload.refreshIntervalMs.v5";
const TREND_RANGES: TrendRange[] = ["1D", "3D", "7D", "15D", "30D"];
const INSPECTOR_INITIAL_LIMIT = 12;
const PROCESS_LEDGER_INITIAL_LIMIT = 40;

type Theme = "dark" | "light";
type RailTab = "projects" | "sessions" | "processes";
type LogTab = "summary" | "evidence" | "trend";
type PopoverView = "online" | "trend";
type TrendRange = "1D" | "3D" | "7D" | "15D" | "30D";
type TrendLane = "history" | "runtime";
type Selection =
  | { type: "overview"; id: "overview" }
  | { type: "project"; id: string }
  | { type: "session"; id: string }
  | { type: "process"; id: string };

type Snapshot = {
  generated_at?: string;
  refresh_slot_id?: string;
  config?: { idle_gap_seconds?: number; process_refresh_target_seconds?: number; history_file?: string };
  current?: CurrentMetrics;
  current_by_tool?: Record<string, CurrentMetrics>;
  summary?: SnapshotSummary;
  coordination_risk?: CoordinationRisk;
  historic_peaks?: { today?: PeakWindow; seven_day?: PeakWindow };
  trends?: TrendSet;
  realtime_trends?: TrendSet;
  history?: { retained_sample_count?: number; loaded_sample_count?: number; last_write_error?: string };
  transcript_stats?: TranscriptStats;
  project_focus?: ProjectSnapshot[];
  candidate_workitems?: CandidateWorkitem[];
  age_buckets?: AgeBucketSnapshot[];
  live_processes?: LiveProcess[];
  live_sessions?: LiveSession[];
  notes?: string[];
};

type CurrentMetrics = {
  active_burst_concurrency?: number;
  session_concurrency?: number;
  pid_concurrency?: number;
};

type SnapshotSummary = {
  active_sessions?: number;
  idle_sessions?: number;
  main_agent_sessions?: number;
  subagent_sessions?: number;
  unknown_role_sessions?: number;
  mapped_processes?: number;
  unmapped_processes?: number;
  multi_mapped_processes?: number;
  project_count?: number;
  hot_project_count?: number;
  mapping_coverage_pct?: number;
};

type CoordinationRisk = {
  top_project?: string;
  top_project_attention_share_pct?: number;
  duplicate_overlap_suspicion_count?: number;
  candidate_workitem_count?: number;
  stale_session_count?: number;
  orphan_process_count?: number;
  low_confidence_session_count?: number;
  recent_window_minutes?: number;
  load_ratio_pct?: number;
  load_peak_value?: number;
  candidate_workitem_coverage_pct?: number;
  candidate_workitem_covered_session_count?: number;
  signals?: Array<{ kind?: string; severity?: string; evidence?: string }>;
};

type PeakWindow = {
  session_concurrency?: { value?: number; at?: string };
  active_burst_concurrency?: { value?: number; at?: string };
};

type TrendSet = { windows?: TrendWindow[] };
type TrendWindow = {
  range?: string;
  from?: string;
  to?: string;
  granularity_seconds?: number;
  source_from?: string;
  source_lookback_hours?: number;
  points?: TrendPoint[];
  history_complete?: boolean;
};
type TrendPoint = {
  at?: string;
  active_burst_concurrency?: number;
  session_concurrency?: number;
  pid_concurrency?: number;
  mapped_processes?: number;
  unmapped_processes?: number;
  mapping_coverage_pct?: number;
  transcript_sampled?: boolean;
  runtime_sampled?: boolean;
};

type TrendPlotPoint = { at: string; x: number; y: number; value: number };
type TrendTimeBand = { tone: "night" | "morning" | "day" | "evening"; x: number; width: number };
type TrendChartModel = {
  primaryPath: string;
  secondaryPath: string;
  primaryAreaPath: string;
  secondaryAreaPath: string;
  points: TrendPlotPoint[];
  secondaryPoints: TrendPlotPoint[];
  timeBands: TrendTimeBand[];
  axis: { start: string; selected?: string; end: string };
};
type RefreshReason = "initial" | "manual" | "auto";
type ActiveElementIdentity =
  | { type: "focus-key"; key: string }
  | { type: "selector"; selector: string };
type ViewportState = {
  windowX: number;
  windowY: number;
  activeElement: HTMLElement | null;
  activeIdentity: ActiveElementIdentity | null;
  scrollTargets: Array<{ selector: string; index: number; top: number; left: number }>;
};

type TranscriptStats = {
  scanned_files?: number;
  parsed_files?: number;
  deferred_files?: number;
  tail_parsed_files?: number;
  historical_scan_deferred?: boolean;
  foreground_scan_lookback_seconds?: number;
  configured_history_lookback_seconds?: number;
  cached?: boolean;
  errors?: string[];
};

type ProjectSnapshot = {
  project?: string;
  session_count?: number;
  active_burst_count?: number;
  main_agent_sessions?: number;
  subagent_sessions?: number;
  unknown_role_sessions?: number;
  process_count?: number;
  attention_share_pct?: number;
  attention_basis?: string;
  stale_session_count?: number;
  recent_session_count?: number;
  confidence?: string;
  confidence_breakdown?: ConfidenceCount[];
  confidence_reasons?: string[];
  project_attribution_confidence?: string;
  project_attribution_reasons?: string[];
  last_event_age_seconds?: number;
  last_event_at?: string;
  tools?: ProjectTool[];
};

type ConfidenceCount = {
  level?: string;
  count?: number;
};

type AgeBucketSnapshot = {
  label?: string;
  count?: number;
};

type ProjectTool = {
  tool?: string;
  session_count?: number;
  active_burst_count?: number;
  process_count?: number;
};

type HostApp = {
  pid?: number;
  name?: string;
  bundle_path?: string;
};

type LiveProcess = {
  pid?: number;
  tool?: string;
  command?: string;
  mapped_sessions?: number;
  session_ids?: string[];
  host_app?: HostApp;
};

type LiveSession = {
  tool?: string;
  session_id?: string;
  session_role?: string;
  role_confidence?: string;
  role_reasons?: string[];
  thread_source?: string;
  parent_thread_id?: string;
  agent_role?: string;
  agent_nickname?: string;
  role_hint_source?: string;
  independently_run?: boolean;
  project?: string;
  process_count?: number;
  host_apps?: HostApp[];
  active_burst?: boolean;
  freshness?: string;
  mapping_method?: string;
  missing_transcript?: boolean;
  confidence?: string;
  confidence_reasons?: string[];
  last_event_age_seconds?: number;
  last_event_at?: string;
  path?: string;
  provenance?: string[];
};

type CandidateWorkitem = {
  key?: string;
  project?: string;
  tool?: string;
  freshness_bucket?: string;
  session_count?: number;
  process_count?: number;
  confidence?: string;
  confidence_reasons?: string[];
};

type RailItem = {
  id: string;
  type: Selection["type"];
  kind: "scan" | "query" | "verify";
  title: string;
  description: string;
  command: string;
  status: "active" | "idle" | "done" | "failed";
  tags: string[];
  value: string;
};
type SessionBranch = { parent: LiveSession; children: LiveSession[] };
type ToolSessionGroup = {
  tool: string;
  sessions: LiveSession[];
  activeCount: number;
  linked: SessionBranch[];
  unlinked: LiveSession[];
  unknown: LiveSession[];
};

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
  const refreshSnapshot = useCallback(async () => {
    setRefreshing(true);
    try {
      const response = await fetch("/api/refresh", { method: "POST", headers: { "Content-Type": "application/json" } });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      await delay(MANUAL_REFRESH_SETTLE_MS);
      await fetchSnapshot("manual");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRefreshing(false);
    }
  }, [fetchSnapshot]);

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
          await fetchSnapshot("auto").catch(() => undefined);
        }
        if (!cancelled) schedule();
      }, delay);
    };
    schedule();
    return () => {
      cancelled = true;
      clearTimer();
    };
  }, [fetchSnapshot, popoverView, refreshInterval, surfaceVersion, view]);
  useEffect(() => {
    const refreshIfStale = () => {
      if (!refreshInterval || !surfaceVisible(view, popoverVisibleRef.current)) return;
      const staleAfter = Math.max(DEFAULT_REFRESH_INTERVAL_MS, refreshInterval);
      if (!snapshotRef.current || Date.now() - lastSnapshotReceivedAtRef.current >= staleAfter) {
        void fetchSnapshot("auto").catch(() => undefined);
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
  }, [fetchSnapshot, refreshInterval, view]);
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

  const selected = useMemo(() => resolveSelection(t, snapshot, selection), [t, snapshot, selection]);
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
              <PopoverAuditShell t={t} snapshot={snapshot} selection={selection} setSelection={setSelection} />
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
  return (
    <footer className="popover-footer">
      <div className="footer-meta">
        <span className={`state-dot ${snapshot ? "observed" : "idle"}`} aria-hidden="true" />
        <span>{generated}</span>
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
        <DashboardFrontTopline t={t} snapshot={snapshot} refreshInterval={refreshInterval} cycleRefreshInterval={cycleRefreshInterval} />
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
            <ProjectAtlas t={t} snapshot={snapshot} selection={selection} setSelection={setSelection} limit={14} defaultExpandedCount={3} showHead={false} />
          </div>
          <DashboardSideRails t={t} snapshot={snapshot} />
        </div>
      </section>

      <section className="dash-ledger-band">
        <DashboardBandHead kicker={t("liveLedger")} title={t("processLedger")} meta={`${snapshot.live_processes?.length ?? 0} ${t("processes")}`} />
        <ProcessLedger t={t} snapshot={snapshot} selection={selection} setSelection={setSelection} />
      </section>

      <TrendSuite
        t={t}
        snapshot={snapshot}
        range={trendRange}
        setRange={setTrendRange}
        trendSelection={trendSelection}
        setTrendSelection={setTrendSelection}
      />

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
  return (
    <div className="dash-field-grid">
      <article className="dash-support-cell burst">
        <span><TermLabel label={t("metricFresh")} tip={t("tipActiveBurst")} /></span>
        <strong>{current.active_burst_concurrency ?? 0}</strong>
        <em>{t("activeBurstHint")}</em>
      </article>
      <div className="dash-support-read">
        <article className="dash-support-cell session">
          <span><TermLabel label={t("metricSessions")} tip={t("tipSessions")} /></span>
          <strong>{current.session_concurrency ?? 0}</strong>
          <em>{t("liveIdle").replace("{live}", String(summary.active_sessions ?? 0)).replace("{idle}", String(summary.idle_sessions ?? 0))}</em>
        </article>
        <article className="dash-support-cell pid">
          <span><TermLabel label={t("metricProcesses")} tip={t("tipPids")} /></span>
          <strong>{current.pid_concurrency ?? 0}</strong>
          <em>{`${summary.mapped_processes ?? 0} ${t("mapped")} / ${summary.unmapped_processes ?? 0} ${t("unmatched")}`}</em>
        </article>
      </div>
    </div>
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
}: {
  t: (key: string) => string;
  snapshot: Snapshot;
  selection: Selection;
  setSelection: (value: Selection) => void;
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
        <ProjectAtlas t={t} snapshot={snapshot} selection={selection} setSelection={setSelection} compact defaultExpandedCount={0} showHead={false} />
      </section>
    </section>
  );
}

function PopoverRuntimeInstrument({ t, snapshot }: { t: (key: string) => string; snapshot: Snapshot }) {
  const current = snapshot.current ?? {};
  const summary = snapshot.summary ?? {};
  const stats = snapshot.transcript_stats ?? {};
  const scale = currentPeerScale(current);
  const rows = [
    {
      key: "burst",
      label: t("metricFresh"),
      tip: t("tipActiveBurst"),
      value: current.active_burst_concurrency ?? 0,
      detail: t("activeBurstHint"),
      pct: pctPart(current.active_burst_concurrency, scale),
    },
    {
      key: "session",
      label: t("metricSessions"),
      tip: t("tipSessions"),
      value: current.session_concurrency ?? 0,
      detail: t("liveIdle")
        .replace("{live}", String(summary.active_sessions ?? 0))
        .replace("{idle}", String(summary.idle_sessions ?? 0)),
      pct: pctPart(current.session_concurrency, scale),
    },
    {
      key: "pid",
      label: t("metricProcesses"),
      tip: t("tipPids"),
      value: current.pid_concurrency ?? 0,
      detail: `${summary.mapped_processes ?? 0} ${t("mapped")} / ${summary.unmapped_processes ?? 0} ${t("unmatched")}`,
      pct: pctPart(current.pid_concurrency, scale),
    },
  ];
  return (
    <section className="popover-instrument" aria-label={t("runtimeField")}>
      <div className="instrument-spine" aria-hidden="true">
        <span />
        <span />
      </div>
      <div className="instrument-topline">
        <span className={`field-status ${statusTone(snapshot)}`}>{metricState(snapshot, t)}</span>
        <span className="instrument-window" title={transcriptScanNote(t, stats)}>
          <Activity size={13} />
          <span>{t("scanWindow")}</span>
          <strong>{formatAge(stats.foreground_scan_lookback_seconds, t)}</strong>
        </span>
      </div>
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
      <CurrentMeaningStrip t={t} snapshot={snapshot} compact />
    </section>
  );
}

function CurrentMeaningStrip({ t, snapshot, compact = false }: { t: (key: string) => string; snapshot: Snapshot; compact?: boolean }) {
  const detailsId = useId();
  const [expanded, setExpanded] = useState(!compact);
  const lead = primaryEvidenceNote(t, snapshot);
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
          <em>{t("metricLegend")}</em>
        </span>
        <span>
          <b>{t("evidenceNote")}</b>
          <em>{t("activeThinkingCaveat")}</em>
        </span>
        <span>
          <b><TermLabel label={t("scanState")} tip={t("tipScanner")} /></b>
          <em>{transcriptScanNote(t, stats)}</em>
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
}: {
  t: (key: string) => string;
  snapshot: Snapshot;
  selection: Selection;
  setSelection: (value: Selection) => void;
  compact?: boolean;
  limit?: number;
  defaultExpandedCount: number;
  showHead?: boolean;
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
  const current = snapshot.current ?? {};
  const scale = currentPeerScale(current);
  return (
    <aside className="dash-atlas-side">
      <section className="dash-side-module">
        <div className="dash-mini-head"><h3>{t("calibration")}</h3><span>{t("currentMeaning")}</span></div>
        <CalibrationRail t={t} snapshot={snapshot} scale={scale} />
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

function CalibrationRail({ t, snapshot, scale }: { t: (key: string) => string; snapshot: Snapshot; scale: number }) {
  const current = snapshot.current ?? {};
  const summary = snapshot.summary ?? {};
  const mappedPct = t("mappedPct").replace("{pct}", formatPct(summary.mapping_coverage_pct));
  const unmatchedCount = t("unmatchedCount").replace("{count}", String(summary.unmapped_processes ?? 0));
  const rows = [
    {
      key: "burst",
      label: t("active"),
      value: current.active_burst_concurrency ?? 0,
      primary: t("activeBurstHint"),
      secondary: t("currentScale"),
      pct: pctPart(current.active_burst_concurrency, scale),
    },
    {
      key: "session",
      label: t("sessions"),
      value: current.session_concurrency ?? 0,
      primary: t("knownSessions"),
      secondary: t("liveIdle")
        .replace("{live}", String(summary.active_sessions ?? 0))
        .replace("{idle}", String(summary.idle_sessions ?? 0)),
      pct: pctPart(current.session_concurrency, scale),
    },
    {
      key: "pid",
      label: t("processes"),
      value: current.pid_concurrency ?? 0,
      primary: t("visibleProcesses"),
      secondary: `${mappedPct} · ${unmatchedCount}`,
      pct: pctPart(current.pid_concurrency, scale),
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
    const level = item.level || t("unknown");
    counts.set(level, (counts.get(level) ?? 0) + (item.count ?? 0));
  });
  if (!counts.size) {
    (snapshot.live_sessions ?? []).forEach((session) => {
      const level = session.confidence || t("unknown");
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
            <ToolIcon tool={item.tool || "unknown"} />
            <span>
              <strong>{item.project || t("unassigned")}</strong>
              <em>{item.freshness_bucket || t("unavailable")} · {item.confidence || t("unavailable")}</em>
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
  const rows = useMemo(() => [...(snapshot.live_processes ?? [])].sort((a, b) => (b.mapped_sessions ?? 0) - (a.mapped_sessions ?? 0)), [snapshot.live_processes]);
  const hiddenCount = Math.max(0, rows.length - PROCESS_LEDGER_INITIAL_LIMIT);
  const visibleRows = showOverflow ? rows : rows.slice(0, PROCESS_LEDGER_INITIAL_LIMIT);
  const overflowLabel = countLabel(t, showOverflow ? "lessCount" : "moreCount", hiddenCount);
  return (
    <div className="process-ledger" role="table" aria-label={t("processLedger")}>
      <div className="process-row head" role="row">
        <span>{t("processes")}</span>
        <span>{t("tools")}</span>
        <span>{t("mapped")}</span>
        <span>{t("sessions")}</span>
        <span>{t("command")}</span>
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
  );
}

function ProcessLedgerRow({ t, process, selection, setSelection }: { t: (key: string) => string; process: LiveProcess; selection: Selection; setSelection: (value: Selection) => void }) {
  const processID = String(process.pid ?? "");
  const sessions = process.session_ids ?? [];
  const host = process.host_app;
  const selected = selection.type === "process" && selection.id === processID;
  return (
    <div className={`process-row process-detail-row ${selected ? "is-selected" : ""}`} role="row">
      <span className="process-cell process-main-cell" role="cell">
        <button className="process-main" type="button" data-focus-key={focusKey("process", processID)} aria-current={selected ? "true" : undefined} onClick={() => setSelection({ type: "process", id: processID })}>
          <Server size={13} />
          <span>{t("pid")} {process.pid ?? t("unavailable")}</span>
        </button>
      </span>
      <span className="process-cell tool-cell" role="cell"><ToolIcon tool={process.tool || "unknown"} />{toolDisplayName(process.tool)}</span>
      <span className={`process-map ${(process.mapped_sessions ?? 0) > 0 ? "mapped" : "unmapped"}`} role="cell">{process.mapped_sessions ?? 0}</span>
      <div className="process-session-preview" role="cell" aria-label={t("sessions")}>
        {sessions.length ? sessions.slice(0, 3).map((sessionID) => (
          <button className="session-chip" type="button" key={sessionID} data-focus-key={focusKey("process-session", processID, sessionID)} onClick={() => setSelection({ type: "session", id: safeID(sessionID) })}>
            {shortID(sessionID)}
          </button>
        )) : <span className="muted-inline">{t("unavailable")}</span>}
        {sessions.length > 3 ? <span className="session-chip more">+{sessions.length - 3}</span> : null}
      </div>
      <code className="process-command" role="cell" title={process.command || ""}>{compactCommand(process.command) || t("unavailable")}</code>
      <span className="process-host-cell" role="cell">
        {host ? <HostAppButton t={t} host={host} /> : null}
        <span>{host?.name || t("unavailable")}</span>
      </span>
    </div>
  );
}

function ToolMix({ t, snapshot }: { t: (key: string) => string; snapshot: Snapshot }) {
  const tools = Object.entries(snapshot.current_by_tool ?? {}).sort((a, b) => (b[1].session_concurrency ?? 0) - (a[1].session_concurrency ?? 0));
  return (
    <div className="tool-mix">
      {tools.length ? tools.slice(0, 4).map(([tool, metrics]) => (
        <span className="tool-mix-item" key={tool}>
          <ToolIcon tool={tool} />
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

function TrendSuite({
  t,
  snapshot,
  compact = false,
  range,
  setRange,
  trendSelection,
  setTrendSelection,
}: {
  t: (key: string) => string;
  snapshot: Snapshot;
  compact?: boolean;
  range: TrendRange;
  setRange: (value: TrendRange) => void;
  trendSelection: Record<TrendLane, string | undefined>;
  setTrendSelection: React.Dispatch<React.SetStateAction<Record<TrendLane, string | undefined>>>;
}) {
  const activeRanges = activeTrendRanges(snapshot);
  const effectiveRange = activeRanges.includes(range) ? range : activeRanges[0] ?? range;
  const history = trendWindowForRange(snapshot.trends, effectiveRange);
  const runtime = trendWindowForRange(snapshot.realtime_trends, effectiveRange);
  return (
    <section className={`trend-suite ${compact ? "compact" : "dashboard"}`}>
      <div className="trend-suite-head">
        <div className="trend-suite-copy">
          <h2>{t("trendSuite")}</h2>
          <span>{effectiveRange} · {formatTrendWindow(t, history ?? runtime)}</span>
        </div>
        <div className="trend-range-switch" role="group" aria-label={t("trend")}>
          {TREND_RANGES.map((item) => (
            <button key={item} type="button" disabled={!activeRanges.includes(item)} aria-pressed={item === effectiveRange} data-focus-key={focusKey("trend-range", item)} onClick={() => setRange(item)}>
              {item}
            </button>
          ))}
        </div>
      </div>
      {history || runtime ? (
        <div className="trend-duo">
          <TrendLaneView t={t} lane="history" title={t("historyLane")} trendWindow={history} compact={compact} selectedAt={trendSelection.history} setTrendSelection={setTrendSelection} />
          <TrendLaneView t={t} lane="runtime" title={t("runtimeLane")} trendWindow={runtime} compact={compact} selectedAt={trendSelection.runtime} setTrendSelection={setTrendSelection} />
        </div>
      ) : (
        <section className="empty-inline"><Gauge size={18} /><span>{t("noTrend")}</span></section>
      )}
    </section>
  );
}

function TrendLaneView({
  t,
  lane,
  title,
  trendWindow,
  compact,
  selectedAt,
  setTrendSelection,
}: {
  t: (key: string) => string;
  lane: TrendLane;
  title: string;
  trendWindow?: TrendWindow;
  compact: boolean;
  selectedAt?: string;
  setTrendSelection: React.Dispatch<React.SetStateAction<Record<TrendLane, string | undefined>>>;
}) {
  const sampleKey = lane === "history" ? "transcript_sampled" : "runtime_sampled";
  const points = sampledPoints(trendWindow, sampleKey);
  const selected = points.find((point) => point.at === selectedAt) ?? points[points.length - 1];
  const chart = trendChart(trendWindow, points, lane);
  const selectedPlot = selected ? chart.points.find((point) => point.at === selected.at) : undefined;
  const calloutX = selectedPlot ? clampNumber(selectedPlot.x > 196 ? selectedPlot.x - 122 : selectedPlot.x + 8, 10, 198) : 0;
  const selectedReadout = selected ? trendSelectedReadout(t, lane, selected) : "";
  return (
    <article className={`trend-lane ${lane}`}>
      <div className="trend-lane-head">
        <div>
          <span className="trend-kicker">{title}</span>
          <small>{trendWindow?.range || t("unavailable")}</small>
        </div>
        <em>{points.length} {t("samples")}</em>
      </div>
      {points.length ? (
        <>
          <div className="trend-chart">
            <svg viewBox="0 0 320 112" role="img" aria-label={title}>
              <g className="trend-time-bands" aria-hidden="true">
                {chart.timeBands.map((band, index) => (
                  <rect className={`time-band ${band.tone}`} key={`${band.tone}-${index}`} x={band.x} y="12" width={band.width} height="80" />
                ))}
              </g>
              <path className="grid" d="M8 12H312M8 52H312M8 92H312" />
              <path className="series-area primary" d={chart.primaryAreaPath} />
              <path className="series-area secondary" d={chart.secondaryAreaPath} />
              <path className="series primary" d={chart.primaryPath} />
              <path className="series secondary" d={chart.secondaryPath} />
              {selectedPlot ? (
                <g className="trend-selection-guide" aria-hidden="true">
                  <line x1={selectedPlot.x} y1="10" x2={selectedPlot.x} y2="94" />
                  <g className="trend-callout" transform={`translate(${calloutX.toFixed(1)} 13)`}>
                    <rect width="112" height="40" rx="5" />
                    <text x="7" y="13">{selected.at ? formatChartAxisLabel(selected.at) : t("selectedValues")}</text>
                    <text className="value" x="7" y="29">{selectedReadout}</text>
                  </g>
                </g>
              ) : null}
              {chart.secondaryPoints.map((item) => (
                <circle className={`trend-point secondary ${selected?.at === item.at ? "is-selected" : ""}`} key={`${lane}-secondary-${item.at}`} cx={item.x} cy={item.y} r="2.5" />
              ))}
              {chart.points.map((item) => (
                <g
                  key={`${lane}-${item.at}`}
                  className="trend-hit"
                  role="button"
                  tabIndex={0}
                  aria-label={`${title} ${formatDateTime(item.at)}`}
                  data-focus-key={focusKey("trend-point", lane, trendWindow?.range || "", item.at || "")}
                  onClick={() => setTrendSelection((current) => ({ ...current, [lane]: item.at }))}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      setTrendSelection((current) => ({ ...current, [lane]: item.at }));
                    }
                  }}
                >
                  <circle className={`trend-point primary ${selected?.at === item.at ? "is-selected" : ""}`} cx={item.x} cy={item.y} r="3" />
                  <circle className={`trend-point-hit ${selected?.at === item.at ? "is-selected" : ""}`} cx={item.x} cy={item.y} r="8" />
                </g>
              ))}
              <g className="chart-axis-labels" aria-hidden="true">
                <text x="8" y="108">{chart.axis.start}</text>
                {selectedPlot && chart.axis.selected && selectedPlot.x > 62 && selectedPlot.x < 258 ? (
                  <text className="selected" x={selectedPlot.x} y="108" textAnchor="middle">{chart.axis.selected}</text>
                ) : null}
                <text x="312" y="108" textAnchor="end">{chart.axis.end}</text>
              </g>
            </svg>
          </div>
          {selected ? <TrendDetail t={t} lane={lane} point={selected} trendWindow={trendWindow} compact={compact} /> : null}
        </>
      ) : (
        <section className="empty-inline"><Gauge size={18} /><span>{t("noTrend")}</span></section>
      )}
    </article>
  );
}

function TrendDetail({ t, lane, point, trendWindow, compact }: { t: (key: string) => string; lane: TrendLane; point: TrendPoint; trendWindow?: TrendWindow; compact: boolean }) {
  const detailsId = useId();
  const [expanded, setExpanded] = useState(!compact);
  const contextMetrics = trendContextMetrics(t, trendWindow);
  const metrics = trendDetailMetrics(t, lane, point);
  const sections = trendExplanationSections(t, lane, point);
  return (
    <div className={`trend-detail ${compact ? "compact" : ""} ${expanded ? "expanded" : ""}`}>
      <div className="trend-detail-strip">
        <div className="trend-detail-stamp">
          <em>{t("trendExactBucket")}</em>
          <strong>{point.at ? formatDateTime(point.at) : t("unavailable")}</strong>
        </div>
        <div className="trend-selected-readout">
          <span>{t("selectedValues")}</span>
          <strong>{trendSelectedReadout(t, lane, point)}</strong>
        </div>
      </div>
      {compact ? (
        <div className="trend-detail-toggle-row">
          <button
            aria-controls={detailsId}
            aria-expanded={expanded}
            aria-label={expanded ? t("collapseDetails") : t("expandDetails")}
            className="disclosure-icon-btn trend-detail-toggle"
            data-focus-key={focusKey("trend-detail", lane, point.at || "", compact ? "compact" : "full")}
            title={expanded ? t("collapseDetails") : t("expandDetails")}
            type="button"
            onClick={() => setExpanded((current) => !current)}
          >
            <Info size={13} aria-hidden="true" />
            <span>{expanded ? t("collapseDetails") : t("expandDetails")}</span>
          </button>
        </div>
      ) : null}
      <div className="trend-detail-details" id={detailsId} hidden={!expanded}>
        <div className="trend-detail-grid trend-context-grid">
          {contextMetrics.map((metric) => (
            <span className="trend-detail-metric context" key={metric.label}>
              <b>{metric.label}</b>
              <strong>{metric.value}</strong>
            </span>
          ))}
        </div>
        <div className="trend-detail-grid">
          {metrics.map((metric) => (
            <span className="trend-detail-metric" key={metric.label}>
              <b>{metric.label}</b>
              <strong>{metric.value}</strong>
            </span>
          ))}
        </div>
        <div className="trend-detail-sections">
          {sections.map((section) => (
            <section key={section.label}>
              <span>{section.label}</span>
              <p>{section.text}</p>
            </section>
          ))}
        </div>
      </div>
    </div>
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
  return (
    <header className="topbar">
      <div className="brand">
        <span className="brand-mark" aria-hidden="true">
          <span />
          <span />
          <span />
        </span>
        <div className="brand-text">
          <span className="brand-name">{BRAND_NAME}</span>
          <span className="brand-sub">{t("sub")}</span>
        </div>
      </div>
      <div className="topbar-meta">
        {compact ? <LocalStatus t={t} /> : <Pill tone="safe">{t("loopback")}</Pill>}
        <Pill tone={error ? "bad" : running ? "running" : "idle"}>{error ? t("failed") : running ? t("running") : t("idle")}</Pill>
        {!compact ? (
          <button className="kbd-hint" type="button" data-focus-key={focusKey("topbar-refresh-interval")} onClick={cycleRefreshInterval} title={t("autoRefresh")}>
            <kbd>{formatRefreshInterval(refreshInterval, t)}</kbd> {t("auto")}
          </button>
        ) : null}
        <button className="icon-btn" type="button" data-focus-key={focusKey("topbar-refresh")} onClick={refreshSnapshot} title={t("refresh")} aria-label={t("refresh")}>
          <RefreshCw size={16} className={running ? "spin" : ""} />
        </button>
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

function LocalStatus({ t }: { t: (key: string) => string }) {
  return (
    <span className="local-status-chip" role="status" title={t("loopback")} aria-label={t("loopback")}>
      <span className="state-dot observed" aria-hidden="true" />
    </span>
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
  ];
  if (!compact) {
    pieces.push({ label: t("scanWindow"), value: formatAge(stats.foreground_scan_lookback_seconds, t) });
  }
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
}) {
  const sessions = sessionsForProject(snapshot, project);
  const counts = projectRoleCounts(project, sessions);
  const projectId = safeID(project.project);
  const title = project.project || t("unassigned");
  const evidenceItems = projectEvidenceItems(t, project, compact);
  const projectAge = formatAge(project.last_event_age_seconds, t);
  const projectMeta = projectAge;
  const selected = selection.type === "project" && selection.id === projectId;
  const selectProject = () => {
    setSelection({ type: "project", id: projectId });
    if (!expanded) {
      onOpen?.();
    }
  };
  const disclosureLabel = expanded ? t("collapseDetails") : t("expandDetails");
  return (
    <article className={`project-tree-row ${expanded ? "expanded" : ""} ${selected ? "is-selected" : ""}`}>
      <div className="project-tree-head">
        <span className="project-rank">{rank ?? "-"}</span>
        <button className="project-disclosure" type="button" onClick={onToggle} aria-expanded={expanded} aria-label={disclosureLabel} title={disclosureLabel}>
          <ChevronDown size={15} aria-hidden="true" />
        </button>
        <button className="project-select" type="button" data-focus-key={focusKey("project", projectId)} onClick={selectProject} aria-current={selected ? "true" : undefined}>
          <span>{title}</span>
          <small>{projectMeta}</small>
        </button>
        {compact ? <ProjectCompactMetrics t={t} counts={counts} processCount={project.process_count ?? 0} /> : <ProjectMetricMatrix t={t} counts={counts} processCount={project.process_count ?? 0} />}
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
          <SessionTree t={t} sessions={sessions} selection={selection} setSelection={setSelection} compact={compact} />
        </>
      ) : null}
    </article>
  );
}

function ProjectCompactMetrics({ t, counts, processCount }: { t: (key: string) => string; counts: RoleCounts; processCount: number }) {
  return (
    <div className="project-compact-metrics" aria-label={t("metricSessions")}>
      <div className="project-compact-table">
        <span />
        <b title={t("main")}>{t("mainShort")}</b>
        <b title={t("subagent")}>{t("subagentShort")}</b>
        <b title={t("total")}>{t("totalShort")}</b>
        <em title={t("active")}>{t("activeShort")}</em>
        <strong title={`${t("active")} ${t("main")}`}>{counts.activeMain}</strong>
        <strong title={`${t("active")} ${t("subagent")}`}>{counts.activeSub}</strong>
        <strong title={`${t("active")} ${t("total")}`}>{counts.activeTotal}</strong>
        <em title={t("all")}>{t("allShort")}</em>
        <strong title={`${t("all")} ${t("main")}`}>{counts.main}</strong>
        <strong title={`${t("all")} ${t("subagent")}`}>{counts.sub}</strong>
        <strong title={`${t("all")} ${t("total")}`}>{counts.total}</strong>
      </div>
      <span className="project-compact-proc" title={t("metricProcesses")}>
        <i>{t("processShort")}</i>
        <strong>{processCount}</strong>
      </span>
    </div>
  );
}

function ProjectMetricMatrix({ t, counts, processCount }: { t: (key: string) => string; counts: RoleCounts; processCount: number }) {
  return (
    <div className="project-matrix" aria-label={t("metricSessions")}>
      <span />
      <b title={t("main")}>{t("main")}</b>
      <b title={t("subagent")}>{t("subagent")}</b>
      <b title={t("total")}>{t("total")}</b>
      <b>{t("active")}</b>
      <strong title={`${t("active")} ${t("main")}`}>{counts.activeMain}</strong>
      <strong title={`${t("active")} ${t("subagent")}`}>{counts.activeSub}</strong>
      <strong title={`${t("active")} ${t("total")}`}>{counts.activeTotal}</strong>
      <b>{t("all")}</b>
      <strong title={`${t("all")} ${t("main")}`}>{counts.main}</strong>
      <strong title={`${t("all")} ${t("subagent")}`}>{counts.sub}</strong>
      <strong title={`${t("all")} ${t("total")}`}>{counts.total}</strong>
      <span className="project-proc" title={t("metricProcesses")}>
        <Server size={12} />
        {processCount}
      </span>
    </div>
  );
}

function ToolStrip({ t, tools }: { t: (key: string) => string; tools: ProjectTool[] }) {
  if (!tools.length) return null;
  return (
    <div className="tool-strip" aria-label={t("tools")}>
      {tools.map((tool) => {
        const toolName = tool.tool || "unknown";
        return (
          <span className="tool-mark" key={toolName} title={`${toolDisplayName(toolName)} · ${tool.active_burst_count ?? 0}/${tool.session_count ?? 0} ${t("metricSessions")}`}>
            <ToolIcon tool={toolName} />
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
}: {
  t: (key: string) => string;
  sessions: LiveSession[];
  selection: Selection;
  setSelection: (value: Selection) => void;
  compact: boolean;
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
              <span><ToolIcon tool={group.tool} />{toolDisplayName(group.tool)}</span>
              <strong>{group.activeCount}/{group.sessions.length}</strong>
            </div>
            {visibleLinked.map((branch) => (
              <section className="session-branch" key={sessionIdentity(branch.parent)}>
                <div className="session-group-head">
                  <span>{branch.parent.agent_nickname || shortID(branch.parent.session_id) || t("main")}</span>
                  <strong>{branch.children.length}</strong>
                </div>
                <SessionLine t={t} session={branch.parent} selection={selection} setSelection={setSelection} compact={compact} />
                {(showOverflow ? branch.children : branch.children.slice(0, childLimit)).map((session) => (
                  <SessionLine key={sessionIdentity(session)} t={t} session={session} selection={selection} setSelection={setSelection} compact={compact} child />
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
                  <SessionLine key={sessionIdentity(session)} t={t} session={session} selection={selection} setSelection={setSelection} compact={compact} />
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
                  <SessionLine key={sessionIdentity(session)} t={t} session={session} selection={selection} setSelection={setSelection} compact={compact} />
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
}: {
  t: (key: string) => string;
  session: LiveSession;
  selection: Selection;
  setSelection: (value: Selection) => void;
  compact: boolean;
  child?: boolean;
}) {
  const role = normalizedRole(session.session_role);
  const sid = session.session_id || "";
  const host = session.host_apps?.[0];
  const evidenceItems = sessionEvidenceItems(t, session, compact);
  const processText = compact ? `${session.process_count ?? 0}p` : `${session.process_count ?? 0} ${t("pid")}`;
  const selected = selection.type === "session" && safeID(sid) === selection.id;
  const title = session.agent_nickname || shortID(sid) || "session";
  const meta = `${formatAge(session.last_event_age_seconds, t)} · ${processText} · ${session.confidence || t("unavailable")}`;
  if (compact) {
    return (
      <div className={`session-line role-${role} ${session.active_burst ? "is-active" : ""} ${selected ? "is-selected" : ""} ${child ? "is-child" : ""}`}>
        <span className="session-role-slot">
          <RoleGlyph t={t} role={role} />
        </span>
        <span className="session-tool-pair">
          <ToolIcon tool={session.tool || "unknown"} />
          {host ? <HostAppButton t={t} host={host} /> : <span className="host-empty" title={t("host")} />}
        </span>
        <span className="session-title">
          <SessionIdControl t={t} sid={sid} title={title} selected={selected} setSelection={setSelection} />
          <button className="session-meta-button" type="button" data-focus-key={focusKey("session-meta", sid || title)} onClick={() => setSelection({ type: "session", id: safeID(sid) })}>
            <small>{meta}</small>
          </button>
        </span>
        <div className="session-evidence-strip" aria-label={t("evidence")}>
          {evidenceItems.map((item) => (
            <span className={`session-evidence-chip ${item.tone ?? ""}`} key={item.label}>
              <b>{item.label}</b>
              <em>{item.value}</em>
            </span>
          ))}
        </div>
      </div>
    );
  }
  return (
    <div className={`session-line role-${role} ${session.active_burst ? "is-active" : ""} ${selected ? "is-selected" : ""} ${child ? "is-child" : ""}`}>
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
        <ToolIcon tool={session.tool || "unknown"} />
        {host ? <HostAppButton t={t} host={host} /> : <span className="host-empty" title={t("host")}>{compact ? "" : t("host")}</span>}
      </span>
      <div className="session-evidence-strip" aria-label={t("evidence")}>
        {evidenceItems.map((item) => (
          <span className={`session-evidence-chip ${item.tone ?? ""}`} key={item.label}>
            <b>{item.label}</b>
            <em>{item.value}</em>
          </span>
        ))}
      </div>
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
    <span className="session-id-control" title={sid || title}>
      <button className="session-id-button" type="button" data-focus-key={focusKey("session", sid || title)} aria-current={selected ? "true" : undefined} onClick={() => setSelection({ type: "session", id: safeID(sid) })}>
        <strong>{title}</strong>
      </button>
      <button
        className="session-copy-inline"
        type="button"
        title={t("copySession")}
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
        <Readout label={t("roleConfidence")} value={session.role_confidence || t("unavailable")} />
        <Readout label={t("mappingMethod")} value={session.mapping_method || t("unavailable")} />
        <Readout label={t("threadSource")} value={session.thread_source || t("unavailable")} />
        <Readout label={t("parentThread")} value={session.parent_thread_id || t("unavailable")} />
        <Readout label={t("roleHint")} value={session.role_hint_source || session.agent_role || t("unavailable")} />
        <Readout label={t("freshness")} value={session.freshness || (session.active_burst ? t("active") : t("idle"))} />
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
        <strong>{toolDisplayName(process.tool)}</strong>
        <span>{t("pid")} {process.pid ?? t("unavailable")}</span>
      </div>
      <div className="entity-grid">
        <Readout label={t("metricMatched")} value={String(process.mapped_sessions ?? 0)} />
        <Readout label={t("sessions")} value={sessionIDsText(t, process.session_ids)} />
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

function ToolIcon({ tool }: { tool?: string }) {
  const iconName = toolIconName(tool);
  if (!iconName) return <span className="tool-fallback">{toolBadgeLabel(tool)}</span>;
  return (
    <span className="tool-icon">
      <img src={`/api/tool-icon/${encodeURIComponent(iconName)}`} alt="" loading="lazy" decoding="async" />
    </span>
  );
}

function HostAppButton({ t, host }: { t: (key: string) => string; host: HostApp }) {
  return (
    <button className="host-app" type="button" data-focus-key={focusKey("host-app", host.pid ?? host.bundle_path ?? host.name ?? "")} title={`${t("openHost")}: ${host.name || host.pid}`} onClick={() => openHostApp(host)}>
      <span className="host-icon">
        <img src={`/api/host-app-icon/${encodeURIComponent(String(host.pid ?? ""))}`} alt="" loading="lazy" decoding="async" />
      </span>
      <ExternalLink size={12} />
    </button>
  );
}

type RoleCounts = {
  main: number;
  sub: number;
  unknown: number;
  total: number;
  activeMain: number;
  activeSub: number;
  activeUnknown: number;
  activeTotal: number;
};

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

function Pill({ tone, children }: { tone: "safe" | "idle" | "running" | "bad"; children: React.ReactNode }) {
  return (
    <span className={`pill pill-${tone}`}>
      <span className="dot" />
      {children}
    </span>
  );
}

type SelectedView = {
  title: string;
  kind: "scan" | "query" | "verify";
  status: "queued" | "running" | "done" | "failed" | "canceled" | "empty";
  command: string;
  summary: Record<string, string | number>;
  details: string[];
};

function resolveSelection(t: (key: string) => string, snapshot: Snapshot | null, selection: Selection): SelectedView {
  if (!snapshot || selection.type === "overview") {
    return {
      title: BRAND_NAME,
      kind: "scan",
      status: snapshot ? "done" : "empty",
      command: "/api/snapshot",
      summary: {},
      details: [],
    };
  }
  if (selection.type === "project") {
    const project = (snapshot.project_focus ?? []).find((item) => safeID(item.project) === selection.id);
    return {
      title: project?.project || t("unassigned"),
      kind: "scan",
      status: (project?.active_burst_count ?? 0) > 0 ? "running" : "done",
      command: `project:${project?.project || "unassigned"}`,
      summary: {
        sessions: project?.session_count ?? 0,
        active: project?.active_burst_count ?? 0,
        processes: project?.process_count ?? 0,
      },
      details: [
        `attention_share_pct=${formatPct(project?.attention_share_pct)}`,
        `confidence=${project?.confidence || "unknown"}`,
        `recent_sessions=${project?.recent_session_count ?? 0}`,
        `stale_sessions=${project?.stale_session_count ?? 0}`,
      ],
    };
  }
  if (selection.type === "session") {
    const session = (snapshot.live_sessions ?? []).find((item) => safeID(item.session_id) === selection.id);
    return {
      title: session?.project || shortID(session?.session_id) || t("session"),
      kind: "query",
      status: session?.active_burst ? "running" : "done",
      command: session?.path || `session:${session?.session_id || "unknown"}`,
      summary: {
        tool: session?.tool || "unknown",
        role: session?.session_role || "unknown",
        processes: session?.process_count ?? 0,
      },
      details: [
        `session_id=${session?.session_id || "unknown"}`,
        `freshness=${session?.freshness || "unknown"}`,
        `mapping_method=${session?.mapping_method || "unknown"}`,
        `confidence=${session?.confidence || "unknown"}`,
      ],
    };
  }
  const process = (snapshot.live_processes ?? []).find((item) => String(item.pid ?? "") === selection.id);
  return {
    title: `${process?.tool || t("process")} · ${process?.pid ?? t("pid")}`,
    kind: "verify",
    status: (process?.mapped_sessions ?? 0) > 0 ? "done" : "failed",
    command: process?.command || `pid:${process?.pid || "unknown"}`,
    summary: {
      pid: process?.pid ?? 0,
      tool: process?.tool || "unknown",
      mapped: process?.mapped_sessions ?? 0,
    },
    details: [
      `session_ids=${(process?.session_ids ?? []).join(",") || "none"}`,
      `host_app=${process?.host_app?.name || "unknown"}`,
      `bundle=${process?.host_app?.bundle_path || "unknown"}`,
    ],
  };
}

function buildRailItems(t: (key: string) => string, snapshot: Snapshot | null, tab: RailTab, query: string): RailItem[] {
  if (!snapshot) return [];
  const needle = query.trim().toLowerCase();
  const filter = (item: RailItem) => !needle || `${item.title} ${item.description} ${item.command} ${item.tags.join(" ")}`.toLowerCase().includes(needle);
  let items: RailItem[];
  if (tab === "projects") {
    items = (snapshot.project_focus ?? []).map((project) => ({
      id: safeID(project.project),
      type: "project",
      kind: "scan",
      title: project.project || t("unassigned"),
      description: `${project.session_count ?? 0} ${t("sessions")} · ${project.process_count ?? 0} ${t("processes")}`,
      command: `attention ${formatPct(project.attention_share_pct)}`,
      status: (project.active_burst_count ?? 0) > 0 ? "active" : "done",
      tags: [`${t("mainShort")} ${project.main_agent_sessions ?? 0}`, `${t("subagentShort")} ${project.subagent_sessions ?? 0}`],
      value: `${project.active_burst_count ?? 0} ${t("fresh")}`,
    }));
  } else if (tab === "sessions") {
    items = (snapshot.live_sessions ?? []).map((session) => ({
      id: safeID(session.session_id),
      type: "session",
      kind: "query",
      title: session.project || shortID(session.session_id) || t("session"),
      description: `${session.tool || t("tool")} · ${session.session_role || t("unknown")} · ${session.freshness || t("unknown")}`,
      command: session.path || shortID(session.session_id) || t("session"),
      status: session.active_burst ? "active" : "done",
      tags: [session.tool || t("tool"), session.session_role || t("unknown")],
      value: formatAge(session.last_event_age_seconds, t),
    }));
  } else {
    items = (snapshot.live_processes ?? []).map((process) => ({
      id: String(process.pid ?? ""),
      type: "process",
      kind: "verify",
      title: `${process.tool || t("tool")} · ${process.pid ?? t("pid")}`,
      description: `${process.mapped_sessions ?? 0} ${t("mappedSessions")}`,
      command: process.command || t("process"),
      status: (process.mapped_sessions ?? 0) > 0 ? "done" : "failed",
      tags: [process.tool || t("tool"), process.host_app?.name || t("hostUnknown")],
      value: `${process.mapped_sessions ?? 0} ${t("mapped")}`,
    }));
  }
  return items.filter(filter);
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

function currentMeaningPoints(t: (key: string) => string, snapshot: Snapshot): string[] {
  const current = snapshot.current ?? {};
  const summary = snapshot.summary ?? {};
  const projectCount = summary.project_count ?? snapshot.project_focus?.length ?? 0;
  return [
    `${current.active_burst_concurrency ?? 0} ${t("active")} / ${current.session_concurrency ?? 0} ${t("sessions")}`,
    `${summary.mapped_processes ?? 0} ${t("mapped")} / ${summary.unmapped_processes ?? 0} ${t("unmatched")}`,
    `${projectCount} ${t("projects")} / ${summary.hot_project_count ?? 0} ${t("active")}`,
  ];
}

function activeWindowLabel(t: (key: string) => string, snapshot: Snapshot): string {
  const seconds = snapshot.config?.idle_gap_seconds;
  if (typeof seconds !== "number" || !Number.isFinite(seconds)) return t("activeWindowUnknown");
  return t("activeWindowDefinition").replace("{window}", formatAge(seconds, t));
}

function dashboardProjectMeta(t: (key: string) => string, snapshot: Snapshot): string {
  const summary = snapshot.summary ?? {};
  const projectCount = summary.project_count ?? snapshot.project_focus?.length ?? 0;
  const hotCount = summary.hot_project_count ?? orderedProjects(snapshot).filter((project) => (project.active_burst_count ?? 0) > 0).length;
  return `${projectCount} ${t("projects")} / ${hotCount} ${t("active")}`;
}

function dashboardProjectLead(t: (key: string) => string, snapshot: Snapshot): string {
  const top = orderedProjects(snapshot)[0];
  if (!top?.project) return t("unavailable");
  const attention = typeof top.attention_share_pct === "number" ? `${formatPct(top.attention_share_pct)} · ` : "";
  return `${attention}${top.project}`;
}

function transcriptScanSummary(t: (key: string) => string, stats: TranscriptStats, retainedSamples?: number): string {
  const parsed = stats.parsed_files ?? 0;
  const scanned = stats.scanned_files ?? 0;
  const deferred = stats.deferred_files ?? 0;
  const tail = stats.tail_parsed_files ?? 0;
  const retained = typeof retainedSamples === "number" ? ` · ${retainedSamples} ${t("samples")}` : "";
  const boundary = stats.historical_scan_deferred
    ? t("historicalWalkDeferred")
    : deferred > 0
      ? `${deferred} ${t("deferred")}`
      : tail > 0
        ? `${tail} ${t("tail")}`
        : "";
  return `${parsed}/${scanned} ${t("localLogs")}${boundary ? ` · ${boundary}` : ""}${retained}`;
}

function transcriptScanNote(t: (key: string) => string, stats: TranscriptStats): string {
  const window = formatAge(stats.foreground_scan_lookback_seconds, t);
  if (stats.historical_scan_deferred) {
    return `${t("historicalWalkDeferred")} · ${t("foregroundWindow")} ${window}`;
  }
  if ((stats.deferred_files ?? 0) > 0) {
    return `${stats.deferred_files ?? 0} ${t("deferred")} · ${t("foregroundWindow")} ${window}`;
  }
  return `${t("foregroundWindowOnly")} · ${t("foregroundWindow")} ${window}`;
}

function deferredScanValue(t: (key: string) => string, stats?: TranscriptStats): string {
  return stats?.historical_scan_deferred ? t("historicalWalkShort") : String(stats?.deferred_files ?? 0);
}

function mappingHealthText(t: (key: string) => string, snapshot: Snapshot): string {
  const summary = snapshot.summary ?? {};
  const current = snapshot.current ?? {};
  return `${summary.mapped_processes ?? 0} ${t("mapped")} / ${summary.unmapped_processes ?? 0} ${t("unmatched")} · ${current.pid_concurrency ?? 0} ${t("processesObserved")}`;
}

function primaryEvidenceNote(t: (key: string) => string, snapshot: Snapshot): string {
  const risk = snapshot.coordination_risk ?? {};
  const summary = snapshot.summary ?? {};
  const firstSignal = risk.signals?.find((signal) => signal.evidence)?.evidence;
  if (firstSignal) return firstSignal;
  const unmapped = risk.orphan_process_count ?? summary.unmapped_processes ?? 0;
  if (unmapped > 0) return `${unmapped} ${t("evidenceNeedsReview")}`;
  const lowConfidence = risk.low_confidence_session_count ?? 0;
  if (lowConfidence > 0) return `${lowConfidence} ${t("lowConfidenceEvidence")}`;
  const mapped = summary.mapped_processes ?? 0;
  const pids = snapshot.current?.pid_concurrency ?? 0;
  if (mapped || pids) return `${mapped}/${pids} ${t("processEvidenceMapped")}`;
  return t("noSignals");
}

function orderedProjects(snapshot: Snapshot): ProjectSnapshot[] {
  return [...(snapshot.project_focus ?? [])].sort((a, b) => {
    const activeDelta = (b.active_burst_count ?? 0) - (a.active_burst_count ?? 0);
    if (activeDelta) return activeDelta;
    const attentionDelta = (b.attention_share_pct ?? 0) - (a.attention_share_pct ?? 0);
    if (attentionDelta) return attentionDelta;
    return String(a.project || "").localeCompare(String(b.project || ""));
  });
}

function projectKey(project?: string): string {
  return String(project || "unassigned").trim() || "unassigned";
}

function sessionsForProject(snapshot: Snapshot, project: ProjectSnapshot): LiveSession[] {
  const key = projectKey(project.project).toLowerCase();
  return [...(snapshot.live_sessions ?? [])]
    .filter((session) => projectKey(session.project).toLowerCase() === key)
    .sort((a, b) => {
      if (Number(Boolean(a.active_burst)) !== Number(Boolean(b.active_burst))) return Number(Boolean(b.active_burst)) - Number(Boolean(a.active_burst));
      const ageA = typeof a.last_event_age_seconds === "number" ? a.last_event_age_seconds : Number.MAX_SAFE_INTEGER;
      const ageB = typeof b.last_event_age_seconds === "number" ? b.last_event_age_seconds : Number.MAX_SAFE_INTEGER;
      if (ageA !== ageB) return ageA - ageB;
      return sessionIdentity(a).localeCompare(sessionIdentity(b));
    });
}

function projectRoleCounts(project: ProjectSnapshot, sessions: LiveSession[]): RoleCounts {
  const counts: RoleCounts = {
    main: project.main_agent_sessions ?? 0,
    sub: project.subagent_sessions ?? 0,
    unknown: project.unknown_role_sessions ?? 0,
    total: project.session_count ?? 0,
    activeMain: 0,
    activeSub: 0,
    activeUnknown: 0,
    activeTotal: project.active_burst_count ?? 0,
  };
  if (sessions.length) {
    counts.main = 0;
    counts.sub = 0;
    counts.unknown = 0;
    counts.activeMain = 0;
    counts.activeSub = 0;
    counts.activeUnknown = 0;
    sessions.forEach((session) => {
      const role = normalizedRole(session.session_role);
      if (role === "main") counts.main++;
      else if (role === "subagent") counts.sub++;
      else counts.unknown++;
      if (session.active_burst) {
        if (role === "main") counts.activeMain++;
        else if (role === "subagent") counts.activeSub++;
        else counts.activeUnknown++;
      }
    });
    counts.total = sessions.length;
    counts.activeTotal = counts.activeMain + counts.activeSub + counts.activeUnknown;
  }
  return counts;
}

function projectEvidenceItems(t: (key: string) => string, project: ProjectSnapshot, compact: boolean): Array<{ label: string; value: string; tone?: string }> {
  const stale = project.stale_session_count ?? 0;
  const recent = project.recent_session_count ?? 0;
  const items = [
    { label: t("attention"), value: formatPct(project.attention_share_pct), tone: (project.attention_share_pct ?? 0) > 50 ? "active" : "" },
    { label: t("basis"), value: project.attention_basis || t("unavailable") },
    { label: t("confidence"), value: project.confidence || t("unavailable"), tone: project.confidence === "high" ? "good" : "" },
    { label: t("attribution"), value: project.project_attribution_confidence || t("unavailable"), tone: project.project_attribution_confidence === "high" ? "good" : "" },
    { label: t("recent"), value: String(recent), tone: recent > 0 ? "active" : "" },
    { label: t("stale"), value: String(stale), tone: stale > 0 ? "warn" : "" },
    { label: t("lastEvent"), value: formatAge(project.last_event_age_seconds, t) },
  ];
  return compact ? items.slice(0, 4) : items;
}

function buildToolSessionGroups(sessions: LiveSession[]): ToolSessionGroup[] {
  const sorted = [...sessions].sort(compareSessionsByFreshness);
  const byID = new Map<string, LiveSession>();
  sorted.forEach((session) => {
    if (session.session_id) byID.set(session.session_id, session);
  });

  type MutableToolSessionGroup = {
    tool: string;
    sessions: LiveSession[];
    activeCount: number;
    mains: LiveSession[];
    childrenByParent: Map<string, LiveSession[]>;
    unlinked: LiveSession[];
    unknown: LiveSession[];
  };
  const groupMap = new Map<string, MutableToolSessionGroup>();
  const ensureGroup = (tool?: string): MutableToolSessionGroup => {
    const key = String(tool || "unknown").trim() || "unknown";
    const existing = groupMap.get(key);
    if (existing) return existing;
    const group: MutableToolSessionGroup = {
      tool: key,
      sessions: [],
      activeCount: 0,
      mains: [],
      childrenByParent: new Map(),
      unlinked: [],
      unknown: [],
    };
    groupMap.set(key, group);
    return group;
  };

  sorted.forEach((session) => {
    const group = ensureGroup(session.tool);
    group.sessions.push(session);
    if (session.active_burst) group.activeCount++;
    if (normalizedRole(session.session_role) === "main") group.mains.push(session);
  });

  sorted.forEach((session) => {
    const role = normalizedRole(session.session_role);
    const group = ensureGroup(session.tool);
    if (role === "subagent") {
      const parent = session.parent_thread_id ? byID.get(session.parent_thread_id) : undefined;
      if (parent && normalizedRole(parent.session_role) === "main") {
        const parentGroup = ensureGroup(parent.tool || session.tool);
        const parentKey = sessionIdentity(parent);
        parentGroup.childrenByParent.set(parentKey, [...(parentGroup.childrenByParent.get(parentKey) ?? []), session]);
      } else {
        group.unlinked.push(session);
      }
      return;
    }
    if (role === "unknown") {
      group.unknown.push(session);
    }
  });

  return Array.from(groupMap.values())
    .map((group) => ({
      tool: group.tool,
      sessions: group.sessions,
      activeCount: group.activeCount,
      linked: group.mains
        .sort(compareSessionsByFreshness)
        .map((parent) => ({
          parent,
          children: (group.childrenByParent.get(sessionIdentity(parent)) ?? []).sort(compareSessionsByFreshness),
        })),
      unlinked: group.unlinked.sort(compareSessionsByFreshness),
      unknown: group.unknown.sort(compareSessionsByFreshness),
    }))
    .sort((a, b) => {
      if (a.activeCount !== b.activeCount) return b.activeCount - a.activeCount;
      if (a.sessions.length !== b.sessions.length) return b.sessions.length - a.sessions.length;
      return a.tool.localeCompare(b.tool);
    });
}

function hiddenToolSessionCount(group: ToolSessionGroup, linkedLimit: number, childLimit: number, unlinkedLimit: number): number {
  const visibleLinked = group.linked.slice(0, linkedLimit);
  const hiddenLinked = group.linked.slice(linkedLimit).reduce((total, branch) => total + 1 + branch.children.length, 0);
  const hiddenChildren = visibleLinked.reduce((total, branch) => total + Math.max(0, branch.children.length - childLimit), 0);
  const visibleUnlinkedCount = Math.min(group.unlinked.length, unlinkedLimit);
  const unknownLimit = Math.max(1, unlinkedLimit - visibleUnlinkedCount);
  const hiddenUnlinked = Math.max(0, group.unlinked.length - visibleUnlinkedCount);
  const hiddenUnknown = Math.max(0, group.unknown.length - unknownLimit);
  return hiddenLinked + hiddenChildren + hiddenUnlinked + hiddenUnknown;
}

function compareSessionsByFreshness(a: LiveSession, b: LiveSession): number {
  if (Number(Boolean(a.active_burst)) !== Number(Boolean(b.active_burst))) return Number(Boolean(b.active_burst)) - Number(Boolean(a.active_burst));
  const ageA = typeof a.last_event_age_seconds === "number" ? a.last_event_age_seconds : Number.MAX_SAFE_INTEGER;
  const ageB = typeof b.last_event_age_seconds === "number" ? b.last_event_age_seconds : Number.MAX_SAFE_INTEGER;
  if (ageA !== ageB) return ageA - ageB;
  return sessionIdentity(a).localeCompare(sessionIdentity(b));
}

function normalizedRole(role?: string): "main" | "subagent" | "unknown" {
  const value = String(role || "").trim().toLowerCase();
  if (value === "main" || value === "main_agent" || value === "user") return "main";
  if (value === "sub" || value === "subagent" || value === "agent") return "subagent";
  return "unknown";
}

function roleLabel(t: (key: string) => string, role: "main" | "subagent" | "unknown"): string {
  return role === "main" ? t("main") : role === "subagent" ? t("subagent") : t("unknown");
}

function RoleGlyph({ t, role }: { t: (key: string) => string; role: "main" | "subagent" | "unknown" }) {
  const label = roleLabel(t, role);
  const icon = role === "main" ? <Terminal size={12} /> : role === "subagent" ? <Bot size={12} /> : <GitBranch size={12} />;
  return (
    <span className="role-glyph" title={label} aria-label={label}>
      {icon}
    </span>
  );
}

function sessionEvidenceItems(t: (key: string) => string, session: LiveSession, compact: boolean): Array<{ label: string; value: string; tone?: string }> {
  const role = normalizedRole(session.session_role);
  const relationship = session.parent_thread_id ? shortID(session.parent_thread_id) : roleLabel(t, role);
  const items = [
    {
      label: t("confidence"),
      value: session.confidence || session.role_confidence || t("unavailable"),
      tone: session.confidence === "high" || session.role_confidence === "high" ? "good" : "",
    },
    { label: t("mappingMethod"), value: session.mapping_method || t("unavailable") },
    { label: session.parent_thread_id ? t("parentThread") : t("threadSource"), value: session.thread_source || relationship },
    { label: t("roleHint"), value: session.role_hint_source || session.agent_role || session.agent_nickname || t("unavailable") },
    { label: t("freshness"), value: session.freshness || (session.active_burst ? t("active") : t("idle")), tone: session.active_burst ? "active" : "" },
  ];
  return compact ? items.slice(0, 3) : items;
}

function sessionIdentity(session: LiveSession): string {
  return session.session_id || session.path || `${session.tool || "tool"}:${session.project || "project"}`;
}

function toolDisplayName(toolName?: string): string {
  const raw = String(toolName || "").trim();
  if (!raw) return "Unknown";
  const key = raw.toLowerCase();
  if (key === "codex" || key === "codexl") return "Codex";
  if (key === "claude") return "Claude";
  if (key === "trae" || key === "traex") return "Trae";
  return raw;
}

function toolIconName(toolName?: string): string {
  const key = String(toolName || "").trim().toLowerCase();
  if (key === "codex" || key === "codexl") return "codex";
  if (key === "claude") return "claude";
  if (key === "trae" || key === "traex") return "trae";
  return "";
}

function toolBadgeLabel(toolName?: string): string {
  const raw = String(toolName || "?").trim();
  return (raw.slice(0, 2) || "?").toUpperCase();
}

function compactCommand(value?: string): string {
  const text = String(value || "").trim();
  if (text.length <= 96) return text;
  return `${text.slice(0, 92)}...`;
}

function sessionIDsText(t: (key: string) => string, ids?: string[]): string {
  if (!ids?.length) return t("unavailable");
  const preview = ids.slice(0, 3).map((id) => shortID(id)).join(", ");
  return ids.length > 3 ? `${preview}, +${ids.length - 3}` : preview;
}

async function openHostApp(host: HostApp) {
  if (!host.pid) return;
  await fetch(`/api/open-host-app/${encodeURIComponent(String(host.pid))}`, { method: "POST" }).catch(() => undefined);
}

function copyText(value: string) {
  if (!value) return;
  void navigator.clipboard?.writeText(value).catch(() => undefined);
}

function renderLogText(t: (key: string) => string, snapshot: Snapshot, selected: SelectedView, tab: LogTab): string {
  if (tab === "summary") {
    return JSON.stringify(
      {
        selected: selected.title,
        status: selected.status,
        metrics: selected.summary,
        current: snapshot.current,
        summary: snapshot.summary,
      },
      null,
      2,
    );
  }
  if (tab === "evidence") {
    const notes = [...(snapshot.notes ?? []), ...(snapshot.transcript_stats?.errors ?? []), ...selected.details];
    if (!notes.length) return `${t("evidence")}: ${t("none")}`;
    return notes.map((line, index) => `${String(index + 1).padStart(2, "0")}  ${line}`).join("\n");
  }
  const history = bestWindow(snapshot.trends);
  const runtime = bestWindow(snapshot.realtime_trends);
  const points = mergeTrendPoints(history?.points ?? [], runtime?.points ?? []).slice(-18);
  return points
    .map((point) => {
      const at = point.at ? formatDateTime(point.at) : t("unavailable");
      return `${at}  fresh=${point.active_burst_concurrency ?? "-"} sessions=${point.session_concurrency ?? "-"} pids=${point.pid_concurrency ?? "-"} mapped=${point.mapped_processes ?? "-"}`;
    })
    .join("\n") || `${t("trend")}: ${t("noTrend")}`;
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

function bestWindow(set?: TrendSet): TrendWindow | undefined {
  return set?.windows?.find((window) => window.range === "1D") ?? set?.windows?.[0];
}

function mergeTrendPoints(history: TrendPoint[], runtime: TrendPoint[]): TrendPoint[] {
  const byAt = new Map<string, TrendPoint>();
  [...history, ...runtime].forEach((point) => {
    const key = point.at || `${byAt.size}`;
    byAt.set(key, { ...(byAt.get(key) ?? {}), ...point, at: key });
  });
  return Array.from(byAt.values()).sort((a, b) => String(a.at).localeCompare(String(b.at)));
}

function activeTrendRanges(snapshot: Snapshot): TrendRange[] {
  return TREND_RANGES.filter((range) => Boolean(trendWindowForRange(snapshot.trends, range) || trendWindowForRange(snapshot.realtime_trends, range)));
}

function trendWindowForRange(set: TrendSet | undefined, range: TrendRange): TrendWindow | undefined {
  return set?.windows?.find((window) => window.range === range);
}

function sampledPoints(window: TrendWindow | undefined, sampledKey: "transcript_sampled" | "runtime_sampled"): TrendPoint[] {
  return [...(window?.points ?? [])].filter((point) => point[sampledKey] || point.at).sort((a, b) => String(a.at).localeCompare(String(b.at)));
}

function trendDetailMetrics(t: (key: string) => string, lane: TrendLane, point: TrendPoint): Array<{ label: string; value: string }> {
  if (lane === "history") {
    return [
      { label: t("metricFresh"), value: trendMetricValue(t, point.active_burst_concurrency) },
      { label: t("metricSessions"), value: trendMetricValue(t, point.session_concurrency) },
    ];
  }
  return [
    { label: t("metricProcesses"), value: trendMetricValue(t, point.pid_concurrency) },
    { label: t("metricMatched"), value: typeof point.mapping_coverage_pct === "number" ? formatPct(point.mapping_coverage_pct) : t("unavailable") },
    { label: t("mappedProcesses"), value: trendMetricValue(t, point.mapped_processes) },
    { label: t("unmappedProcesses"), value: trendMetricValue(t, point.unmapped_processes) },
  ];
}

function trendMetricValue(t: (key: string) => string, value?: number): string {
  return typeof value === "number" && Number.isFinite(value) ? String(value) : t("unavailable");
}

function trendSelectedReadout(t: (key: string) => string, lane: TrendLane, point: TrendPoint): string {
  if (lane === "history") {
    return `${trendMetricValue(t, point.active_burst_concurrency)} / ${trendMetricValue(t, point.session_concurrency)}`;
  }
  return `${trendMetricValue(t, point.pid_concurrency)} / ${typeof point.mapping_coverage_pct === "number" ? formatPct(point.mapping_coverage_pct) : t("unavailable")}`;
}

function trendContextMetrics(t: (key: string) => string, window?: TrendWindow): Array<{ label: string; value: string }> {
  const granularity = typeof window?.granularity_seconds === "number" && window.granularity_seconds > 0 ? formatAge(window.granularity_seconds, t) : t("unavailable");
  const sourceLookback = typeof window?.source_lookback_hours === "number" && Number.isFinite(window.source_lookback_hours) ? `${window.source_lookback_hours}h` : "";
  const source = window?.source_from ? `${formatDateTime(window.source_from)}${sourceLookback ? ` · ${sourceLookback}` : ""}` : sourceLookback || t("unavailable");
  const completeness = window?.history_complete === true ? t("complete") : window?.history_complete === false ? t("partial") : t("unavailable");
  return [
    { label: t("trendRange"), value: window?.range || t("unavailable") },
    { label: t("trendBucket"), value: granularity },
    { label: t("trendWindow"), value: formatTrendWindow(t, window) },
    { label: t("trendSourceWindow"), value: source },
    { label: t("trendCompleteness"), value: completeness },
  ];
}

function trendExplanationSections(t: (key: string) => string, lane: TrendLane, point: TrendPoint): Array<{ label: string; text: string }> {
  const clicked = `${t("sampledBucket")} ${point.at ? formatDateTime(point.at) : t("unavailable")} · ${trendSelectedReadout(t, lane, point)}`;
  return [
    { label: t("trendWhatClicked"), text: clicked },
    { label: t("trendWhatMeans"), text: lane === "history" ? t("trendHistoryMeaning") : t("trendRuntimeMeaning") },
    { label: t("whyTrust"), text: lane === "history" ? t("trendHistoryTrust") : t("trendRuntimeTrust") },
    { label: t("trendHowUse"), text: lane === "history" ? t("trendHistoryUse") : t("trendRuntimeUse") },
  ];
}

function formatTrendWindow(t: (key: string) => string, window: TrendWindow | undefined): string {
  const points = window?.points ?? [];
  if (!points.length) return t("unavailable");
  const firstAt = points[0]?.at;
  const lastAt = points[points.length - 1]?.at;
  const first = firstAt ? formatDateTime(firstAt) : "";
  const last = lastAt ? formatDateTime(lastAt) : "";
  return first && last ? `${first} -> ${last}` : window?.range || t("unavailable");
}

function trendChart(window: TrendWindow | undefined, points: TrendPoint[], lane: TrendLane): TrendChartModel {
  const primaryKey = lane === "history" ? "active_burst_concurrency" : "pid_concurrency";
  const secondaryKey = lane === "history" ? "session_concurrency" : "mapping_coverage_pct";
  const frame = trendChartFrame(window, points);
  const max = Math.max(
    1,
    ...points.map((point) => trendNumericValue(point, primaryKey) ?? 0),
    ...points.map((point) => lane === "runtime" && secondaryKey === "mapping_coverage_pct" ? 100 : trendNumericValue(point, secondaryKey) ?? 0),
  );
  const coordinates = (key: keyof TrendPoint, fixedMax = max): TrendPlotPoint[] => points.flatMap((point, index) => {
    const x = trendPointX(frame, point, index, points.length);
    const value = trendNumericValue(point, key);
    if (value === null) return [];
    const y = 92 - Math.max(0, Math.min(1, value / fixedMax)) * 80;
    return [{ at: point.at || String(index), x, y, value }];
  });
  const primary = coordinates(primaryKey);
  const secondary = coordinates(secondaryKey, lane === "runtime" && secondaryKey === "mapping_coverage_pct" ? 100 : max);
  const gapMs = Math.max(1, (window?.granularity_seconds ?? 0) * 1000 * 1.7);
  const selected = points[points.length - 1];
  const firstAt = points[0]?.at;
  const lastAt = points[points.length - 1]?.at;
  return {
    primaryPath: segmentedLinePath(primary, gapMs),
    secondaryPath: segmentedLinePath(secondary, gapMs),
    primaryAreaPath: segmentedAreaPath(primary, gapMs),
    secondaryAreaPath: segmentedAreaPath(secondary, gapMs),
    points: primary,
    secondaryPoints: secondary,
    timeBands: trendTimeBands(frame),
    axis: {
      start: frame ? formatChartAxisLabel(new Date(frame.fromMs).toISOString()) : firstAt ? formatChartAxisLabel(firstAt) : "",
      selected: selected?.at ? formatChartHour(selected.at) : undefined,
      end: frame ? formatChartAxisLabel(new Date(frame.toMs).toISOString()) : lastAt ? formatChartAxisLabel(lastAt) : "",
    },
  };
}

function trendNumericValue(point: TrendPoint, key: keyof TrendPoint): number | null {
  const value = point[key];
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function trendChartFrame(window: TrendWindow | undefined, points: TrendPoint[]): { fromMs: number; toMs: number; spanMs: number } | null {
  const pointTimes = points.map((point) => pointTimeMs(point.at)).filter((value): value is number => value !== null);
  const fromMs = pointTimeMs(window?.from) ?? (pointTimes.length ? Math.min(...pointTimes) : null);
  const toMs = pointTimeMs(window?.to) ?? (pointTimes.length ? Math.max(...pointTimes) : null);
  if (fromMs === null || toMs === null || toMs <= fromMs) return null;
  return { fromMs, toMs, spanMs: toMs - fromMs };
}

function pointTimeMs(value?: string): number | null {
  if (!value) return null;
  const time = new Date(value).getTime();
  return Number.isFinite(time) ? time : null;
}

function trendPointX(frame: { fromMs: number; spanMs: number } | null, point: TrendPoint, index: number, total: number): number {
  const time = pointTimeMs(point.at);
  if (frame && time !== null) {
    const ratio = Math.max(0, Math.min(1, (time - frame.fromMs) / frame.spanMs));
    return 8 + ratio * 304;
  }
  return total <= 1 ? 160 : 8 + (index / (total - 1)) * 304;
}

function linePath(points: TrendPlotPoint[]): string {
  if (!points.length) return "";
  return points.map((point, index) => `${index === 0 ? "M" : "L"}${point.x.toFixed(1)} ${point.y.toFixed(1)}`).join("");
}

function segmentedLinePath(points: TrendPlotPoint[], gapMs: number): string {
  return trendPointSegments(points, gapMs).map(linePath).join(" ");
}

function segmentedAreaPath(points: TrendPlotPoint[], gapMs: number): string {
  return trendPointSegments(points, gapMs)
    .filter((segment) => segment.length >= 2)
    .map((segment) => {
      const first = segment[0];
      const last = segment[segment.length - 1];
      return `${linePath(segment)} L${last.x.toFixed(1)} 92 L${first.x.toFixed(1)} 92 Z`;
    })
    .join(" ");
}

function trendPointSegments(points: TrendPlotPoint[], gapMs: number): TrendPlotPoint[][] {
  if (!points.length) return [];
  if (!gapMs || !Number.isFinite(gapMs)) return [points];
  const segments: TrendPlotPoint[][] = [];
  let current: TrendPlotPoint[] = [];
  let previousTime: number | null = null;
  points.forEach((point) => {
    const time = pointTimeMs(point.at);
    if (time !== null && previousTime !== null && time - previousTime > gapMs && current.length) {
      segments.push(current);
      current = [];
    }
    current.push(point);
    previousTime = time;
  });
  if (current.length) segments.push(current);
  return segments;
}

function trendTimeBands(frame: { fromMs: number; toMs: number; spanMs: number } | null): TrendTimeBand[] {
  if (!frame) return [];
  const bands: TrendTimeBand[] = [];
  let cursor = new Date(frame.fromMs);
  let guard = 0;
  while (cursor.getTime() < frame.toMs && guard < 240) {
    const startMs = Math.max(cursor.getTime(), frame.fromMs);
    const next = nextTimeToneBoundary(cursor);
    const endMs = Math.min(next.getTime(), frame.toMs);
    if (endMs > startMs) {
      const x = 8 + ((startMs - frame.fromMs) / frame.spanMs) * 304;
      const width = Math.max(.5, ((endMs - startMs) / frame.spanMs) * 304);
      bands.push({ tone: timeOfDayTone(cursor), x, width });
    }
    cursor = next;
    guard += 1;
  }
  return bands;
}

function timeOfDayTone(date: Date): TrendTimeBand["tone"] {
  const hour = date.getHours();
  if (hour < 6) return "night";
  if (hour < 12) return "morning";
  if (hour < 18) return "day";
  return "evening";
}

function nextTimeToneBoundary(date: Date): Date {
  const next = new Date(date);
  const hour = date.getHours();
  if (hour < 6) next.setHours(6, 0, 0, 0);
  else if (hour < 12) next.setHours(12, 0, 0, 0);
  else if (hour < 18) next.setHours(18, 0, 0, 0);
  else {
    next.setDate(next.getDate() + 1);
    next.setHours(0, 0, 0, 0);
  }
  return next;
}

function currentPeerScale(current: CurrentMetrics): number {
  return Math.max(1, current.active_burst_concurrency ?? 0, current.session_concurrency ?? 0, current.pid_concurrency ?? 0);
}

function pctPart(value: number | undefined, total: number): number {
  if (!value || total <= 0) return 0;
  return (value / total) * 100;
}

function clampPct(value: number, min = 0): number {
  if (!Number.isFinite(value)) return min;
  return Math.max(min, Math.min(100, value));
}

function clampNumber(value: number, min: number, max: number): number {
  if (!Number.isFinite(value)) return min;
  return Math.max(min, Math.min(max, value));
}

function statusTone(snapshot: Snapshot): "active" | "idle" | "warn" {
  if ((snapshot.current?.active_burst_concurrency ?? 0) > 0) return "active";
  if ((snapshot.transcript_stats?.errors?.length ?? 0) > 0) return "warn";
  return "idle";
}

function metricState(snapshot: Snapshot, t: (key: string) => string): string {
  if ((snapshot.current?.active_burst_concurrency ?? 0) > 0) return t("active");
  return t("idle");
}

function coordinationPostureLabel(snapshot: Snapshot, t: (key: string) => string): string {
  const risk = snapshot.coordination_risk ?? {};
  const signals = risk.signals?.length ?? 0;
  if (signals > 0) return t("signalsCount").replace("{count}", String(signals));
  if ((risk.low_confidence_session_count ?? 0) > 0) return t("lowConfidenceCount").replace("{count}", String(risk.low_confidence_session_count));
  return t("fresh");
}

function formatPct(value?: number): string {
  if (typeof value !== "number" || Number.isNaN(value)) return "0%";
  return `${Math.round(value)}%`;
}

function formatDateTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function formatChartAxisLabel(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return `${date.toLocaleDateString([], { month: "numeric", day: "numeric" })} ${date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
}

function formatChartHour(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function formatAge(seconds?: number, t?: (key: string) => string): string {
  if (typeof seconds !== "number" || seconds < 0) return t ? t("unavailable") : "n/a";
  if (seconds < 60) return `${Math.round(seconds)}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h`;
  return `${Math.round(seconds / 86400)}d`;
}

function formatRefreshInterval(ms: number, t: (key: string) => string): string {
  if (!ms) return t("refreshPaused");
  return formatAge(ms / 1000, t);
}

function countLabel(t: (key: string) => string, key: string, count: number): string {
  return t(key).replace("{count}", String(count));
}

function shortID(value?: string): string {
  if (!value) return "";
  return value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value;
}

function safeID(value?: string): string {
  const text = String(value || "unassigned").trim();
  return text || "unassigned";
}

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
