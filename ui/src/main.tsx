import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { Activity, ArrowUpRight, Bot, ChevronDown, Copy, ExternalLink, Gauge, GitBranch, Languages, Layers, Moon, RefreshCw, Search, Server, Sun, Terminal, X } from "lucide-react";
import "./styles.css";

const BRAND_NAME = "Agent Load";
const ACTIVE = new Set(["active", "running", "queued"]);
const REFRESH_INTERVALS_MS = [30_000, 60_000, 120_000, 300_000, 0] as const;
const REFRESH_INTERVAL_STORAGE_KEY = "agentload.refreshIntervalMs.v4";

type Lang = "en" | "zh" | "ja";
type Theme = "dark" | "light";
type RailTab = "projects" | "sessions" | "processes";
type LogTab = "summary" | "evidence" | "trend";
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
  signals?: Array<{ kind?: string; severity?: string; evidence?: string }>;
};

type PeakWindow = {
  session_concurrency?: { value?: number; at?: string };
  active_burst_concurrency?: { value?: number; at?: string };
};

type TrendSet = { windows?: TrendWindow[] };
type TrendWindow = { range?: string; points?: TrendPoint[]; history_complete?: boolean };
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
  confidence_reasons?: string[];
  project_attribution_confidence?: string;
  project_attribution_reasons?: string[];
  last_event_age_seconds?: number;
  last_event_at?: string;
  tools?: ProjectTool[];
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

const copy: Record<Lang, Record<string, string>> = {
  en: {
    sub: "Local Agent Console",
    loopback: "127.0.0.1 · no upload",
    idle: "idle",
    running: "refreshing",
    failed: "snapshot failed",
    search: "Filter observations...",
    projects: "Projects",
    sessions: "Sessions",
    processes: "Processes",
    overview: "Overview",
    refresh: "Refresh",
    dashboard: "Dashboard",
    close: "Close",
    inspect: "Inspect",
    metricFresh: "Fresh movement",
    metricSessions: "Sessions",
    metricProcesses: "Processes",
    metricMatched: "Matched share",
    resultKind: "Result kind",
    source: "Local source",
    samples: "Samples",
    scan: "Scan",
    mapping: "Mapping",
    history: "History",
    summary: "Summary",
    evidence: "Evidence",
    trend: "Trend",
    noData: "Waiting for local evidence",
    empty: "Select an observation to inspect it",
    emptySub: "Projects, sessions, and processes are grouped from local process and transcript evidence.",
    command: "Evidence path",
    topProject: "Top project",
    peak: "Observed peak",
    main: "main",
    subagent: "sub",
    unknown: "unknown",
    active: "Active",
    all: "All",
    total: "Total",
    host: "Host",
    tools: "Tools",
    foreground: "Foreground",
    deferred: "Deferred",
    tail: "Tail",
    cached: "cached",
    fresh: "fresh",
    openHost: "Open host app",
    copySession: "Copy session id",
    unassigned: "Unassigned",
    linkedSubagents: "Linked subagents",
    unlinkedSubagents: "Unlinked subagents",
    scanWindow: "Foreground window",
  },
  zh: {
    sub: "本地 Agent 控制台",
    loopback: "127.0.0.1 · 不上传",
    idle: "空闲",
    running: "刷新中",
    failed: "快照失败",
    search: "筛选观测对象...",
    projects: "项目",
    sessions: "会话",
    processes: "进程",
    overview: "概览",
    refresh: "刷新",
    dashboard: "控制台",
    close: "关闭",
    inspect: "查看",
    metricFresh: "最近动作",
    metricSessions: "会话",
    metricProcesses: "进程",
    metricMatched: "匹配占比",
    resultKind: "结果类型",
    source: "本地来源",
    samples: "采样",
    scan: "扫描",
    mapping: "映射",
    history: "历史",
    summary: "摘要",
    evidence: "证据",
    trend: "趋势",
    noData: "等待本地证据",
    empty: "选择一个观测对象",
    emptySub: "项目、会话和进程来自本地进程与活动记录证据。",
    command: "证据路径",
    topProject: "焦点项目",
    peak: "观测峰值",
    main: "主",
    subagent: "子",
    unknown: "未知",
    active: "活跃",
    all: "全部",
    total: "合计",
    host: "宿主",
    tools: "工具",
    foreground: "前台",
    deferred: "延后",
    tail: "尾部",
    cached: "缓存",
    fresh: "新扫",
    openHost: "打开宿主应用",
    copySession: "复制会话 ID",
    unassigned: "未归属",
    linkedSubagents: "已关联子会话",
    unlinkedSubagents: "未关联子会话",
    scanWindow: "前台窗口",
  },
  ja: {
    sub: "ローカル Agent コンソール",
    loopback: "127.0.0.1 · アップロードなし",
    idle: "待機",
    running: "更新中",
    failed: "取得失敗",
    search: "観測対象を絞り込み...",
    projects: "プロジェクト",
    sessions: "セッション",
    processes: "プロセス",
    overview: "概要",
    refresh: "更新",
    dashboard: "ダッシュボード",
    close: "閉じる",
    inspect: "検査",
    metricFresh: "最近ログ動き",
    metricSessions: "セッション",
    metricProcesses: "プロセス",
    metricMatched: "対応率",
    resultKind: "結果種別",
    source: "ローカルソース",
    samples: "サンプル",
    scan: "スキャン",
    mapping: "対応付け",
    history: "履歴",
    summary: "概要",
    evidence: "証拠",
    trend: "推移",
    noData: "ローカル証拠待ち",
    empty: "観測対象を選択",
    emptySub: "プロジェクト、セッション、プロセスはローカル証拠から構成されます。",
    command: "証拠パス",
    topProject: "注目プロジェクト",
    peak: "観測ピーク",
    main: "main",
    subagent: "sub",
    unknown: "unknown",
    active: "Active",
    all: "All",
    total: "Total",
    host: "Host",
    tools: "Tools",
    foreground: "Foreground",
    deferred: "Deferred",
    tail: "Tail",
    cached: "cache",
    fresh: "fresh",
    openHost: "ホストアプリを開く",
    copySession: "セッション ID をコピー",
    unassigned: "Unassigned",
    linkedSubagents: "Linked subagents",
    unlinkedSubagents: "Unlinked subagents",
    scanWindow: "Foreground window",
  },
};

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
  const [refreshInterval, setRefreshInterval] = useState<number>(() => initialRefreshInterval());
  const shellRef = useRef<HTMLDivElement | null>(null);
  const lastRenderTokenRef = useRef("");

  const t = useCallback((key: string) => copy[lang][key] || copy.en[key] || key, [lang]);
  const fetchSnapshot = useCallback(async (reason: "initial" | "manual" | "auto" = "auto") => {
    const response = await fetch("/api/snapshot", { cache: "no-store" });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const next = (await response.json()) as Snapshot;
    const token = next.refresh_slot_id || next.generated_at || "";
    if (reason === "auto" && token && token === lastRenderTokenRef.current) {
      setError(null);
      return;
    }
    lastRenderTokenRef.current = token;
    setSnapshot(next);
    setError(null);
  }, []);
  const refreshSnapshot = useCallback(async () => {
    setRefreshing(true);
    try {
      await fetch("/api/refresh", { method: "POST", headers: { "Content-Type": "application/json" } });
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
    if (!refreshInterval) return;
    const id = window.setInterval(() => {
      if (document.visibilityState === "visible") void fetchSnapshot("auto").catch(() => undefined);
    }, refreshInterval);
    return () => window.clearInterval(id);
  }, [fetchSnapshot, refreshInterval]);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    window.localStorage.setItem("agentload.theme", theme);
  }, [theme]);
  useEffect(() => {
    document.documentElement.lang = lang;
    window.localStorage.setItem("agentload.lang", lang);
  }, [lang]);
  useEffect(() => {
    if (view !== "popover") return;
    const target = shellRef.current;
    if (!target) return;
    const postResize = () => {
      const height = Math.ceil(Math.min(Math.max(target.scrollHeight || 420, 320), 580));
      window.webkit?.messageHandlers?.agentLoadResize?.postMessage({ height });
    };
    postResize();
    const observer = new ResizeObserver(postResize);
    observer.observe(target);
    window.addEventListener("agentLoadPopoverShown", postResize);
    return () => {
      observer.disconnect();
      window.removeEventListener("agentLoadPopoverShown", postResize);
    };
  }, [snapshot, selection, railTab, logTab, view]);

  const railItems = useMemo(() => buildRailItems(snapshot, railTab, query), [snapshot, railTab, query]);
  const selected = useMemo(() => resolveSelection(snapshot, selection), [snapshot, selection]);
  const compact = view === "popover";
  const running = refreshing;

  const cycleRefreshInterval = () => {
    const index = REFRESH_INTERVALS_MS.indexOf(refreshInterval as (typeof REFRESH_INTERVALS_MS)[number]);
    const next = REFRESH_INTERVALS_MS[(index + 1) % REFRESH_INTERVALS_MS.length];
    window.localStorage.setItem(REFRESH_INTERVAL_STORAGE_KEY, String(next));
    setRefreshInterval(next);
  };

  return (
    <div className="app" ref={shellRef}>
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
      <div className="layout">
        <Rail
          t={t}
          activeTab={railTab}
          setActiveTab={setRailTab}
          query={query}
          setQuery={setQuery}
          items={railItems}
          selection={selection}
          setSelection={setSelection}
          compact={compact}
        />
        <Pane
          t={t}
          snapshot={snapshot}
          selected={selected}
          selection={selection}
          setSelection={setSelection}
          logTab={logTab}
          setLogTab={setLogTab}
          refreshSnapshot={refreshSnapshot}
          compact={compact}
        />
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
        {!compact ? <Pill tone="safe">{t("loopback")}</Pill> : null}
        <Pill tone={error ? "bad" : running ? "running" : "idle"}>{error ? t("failed") : running ? t("running") : t("idle")}</Pill>
        {!compact ? (
          <button className="kbd-hint" type="button" onClick={cycleRefreshInterval} title="Auto refresh">
            <kbd>{formatRefreshInterval(refreshInterval, t)}</kbd> auto
          </button>
        ) : null}
        <button className="icon-btn" type="button" onClick={refreshSnapshot} title={t("refresh")} aria-label={t("refresh")}>
          <RefreshCw size={16} className={running ? "spin" : ""} />
        </button>
        <LanguageControl lang={lang} setLang={setLang} />
        <button className="icon-btn" type="button" onClick={() => setTheme(theme === "light" ? "dark" : "light")} title="Toggle theme" aria-label="Toggle theme">
          {theme === "light" ? <Moon size={16} /> : <Sun size={16} />}
        </button>
        {compact ? (
          <>
            <button className="icon-btn" type="button" onClick={() => postHostAction("open_dashboard")} title={t("dashboard")} aria-label={t("dashboard")}>
              <ArrowUpRight size={16} />
            </button>
            <button className="icon-btn" type="button" onClick={() => postHostAction("close")} title={t("close")} aria-label={t("close")}>
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
          <button key={tab.id} className={`rail-tab ${activeTab === tab.id ? "is-active" : ""}`} type="button" role="tab" onClick={() => setActiveTab(tab.id)}>
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
          <button className={`act ${selection.type === "overview" ? "is-selected" : ""}`} data-kind="scan" type="button" onClick={() => setSelection({ type: "overview", id: "overview" })}>
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
            <button className="ghost-btn" type="button" onClick={refreshSnapshot}>{t("refresh")}</button>
            {!compact ? <button className="ghost-btn" type="button" onClick={() => setSelection({ type: "overview", id: "overview" })}>{t("overview")}</button> : null}
          </div>
        </div>
        <Timeline t={t} snapshot={snapshot} selected={selected} />
        <Metrics t={t} snapshot={snapshot} selected={selected} compact={compact} />
        <ProjectWorkspace t={t} snapshot={snapshot} selection={selection} setSelection={setSelection} compact={compact} />
        <div className="logwrap">
          <div className="logtabs">
            {(["summary", "evidence", "trend"] as LogTab[]).map((tab) => (
              <button className={`logtab ${logTab === tab ? "is-active" : ""}`} type="button" key={tab} onClick={() => setLogTab(tab)}>
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
  const projects = orderedProjects(snapshot);
  const selectedProject = selection.type === "project" ? projects.find((project) => safeID(project.project) === selection.id) : undefined;
  const selectedSession = selection.type === "session" ? (snapshot.live_sessions ?? []).find((session) => safeID(session.session_id) === selection.id) : undefined;
  const selectedProcess = selection.type === "process" ? (snapshot.live_processes ?? []).find((process) => String(process.pid ?? "") === selection.id) : undefined;

  if (selection.type === "session" && selectedSession) {
    return <SessionEvidencePanel t={t} session={selectedSession} />;
  }
  if (selection.type === "process" && selectedProcess) {
    return <ProcessEvidencePanel t={t} process={selectedProcess} />;
  }
  if (selectedProject) {
    return (
      <div className="workspace">
        <ScanBoundary t={t} snapshot={snapshot} compact={compact} />
        <ProjectTreeRow
          t={t}
          snapshot={snapshot}
          project={selectedProject}
          setSelection={setSelection}
          compact={compact}
          expanded
        />
      </div>
    );
  }
  return (
    <div className="workspace">
      <ScanBoundary t={t} snapshot={snapshot} compact={compact} />
      <div className="project-tree-list">
        {projects.length ? projects.map((project, index) => (
          <ProjectTreeRow
            key={safeID(project.project)}
            t={t}
            snapshot={snapshot}
            project={project}
            setSelection={setSelection}
            compact={compact}
            expanded={!compact && index < 2}
          />
        )) : (
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
    { label: t("foreground"), value: `${stats.parsed_files ?? 0}/${stats.scanned_files ?? 0}` },
    { label: t("deferred"), value: stats.historical_scan_deferred ? "walk" : String(stats.deferred_files ?? 0) },
    { label: t("tail"), value: stats.tail_parsed_files ?? 0 },
    { label: t("source"), value: stats.cached ? t("cached") : t("fresh") },
  ];
  if (!compact) {
    pieces.push({ label: t("scanWindow"), value: formatAge(stats.foreground_scan_lookback_seconds) });
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
  setSelection,
  compact,
  expanded,
}: {
  t: (key: string) => string;
  snapshot: Snapshot;
  project: ProjectSnapshot;
  setSelection: (value: Selection) => void;
  compact: boolean;
  expanded: boolean;
}) {
  const sessions = sessionsForProject(snapshot, project);
  const counts = projectRoleCounts(project, sessions);
  const projectId = safeID(project.project);
  const title = project.project || t("unassigned");
  return (
    <article className={`project-tree-row ${expanded ? "expanded" : ""}`}>
      <div className="project-tree-head">
        <button className="project-select" type="button" onClick={() => setSelection({ type: "project", id: projectId })}>
          <ChevronDown size={15} aria-hidden="true" />
          <span>{title}</span>
          <small>{formatAge(project.last_event_age_seconds)}</small>
        </button>
        <ProjectMetricMatrix t={t} counts={counts} processCount={project.process_count ?? 0} />
        <ToolStrip t={t} tools={project.tools ?? []} />
      </div>
      {expanded ? <SessionTree t={t} sessions={sessions} setSelection={setSelection} compact={compact} /> : null}
    </article>
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
            <small>{tool.session_count ?? 0}</small>
          </span>
        );
      })}
    </div>
  );
}

function SessionTree({
  t,
  sessions,
  setSelection,
  compact,
}: {
  t: (key: string) => string;
  sessions: LiveSession[];
  setSelection: (value: Selection) => void;
  compact: boolean;
}) {
  const grouped = groupSessionsByTool(sessions);
  if (!sessions.length) {
    return <div className="session-tree empty">{t("empty")}</div>;
  }
  return (
    <div className="session-tree">
      {grouped.map((group) => (
        <section className="session-group" key={group.tool}>
          <div className="session-group-head">
            <span><ToolIcon tool={group.tool} />{toolDisplayName(group.tool)}</span>
            <strong>{group.sessions.length}</strong>
          </div>
          {group.sessions.slice(0, compact ? 5 : 12).map((session) => (
            <SessionLine key={sessionIdentity(session)} t={t} session={session} setSelection={setSelection} compact={compact} />
          ))}
        </section>
      ))}
    </div>
  );
}

function SessionLine({
  t,
  session,
  setSelection,
  compact,
}: {
  t: (key: string) => string;
  session: LiveSession;
  setSelection: (value: Selection) => void;
  compact: boolean;
}) {
  const role = normalizedRole(session.session_role);
  const sid = session.session_id || "";
  const host = session.host_apps?.[0];
  return (
    <div className={`session-line role-${role} ${session.active_burst ? "is-active" : ""}`}>
      <button className="session-main" type="button" onClick={() => setSelection({ type: "session", id: safeID(sid) })}>
        <span className="role-glyph" title={roleLabel(t, role)}>{roleGlyph(role)}</span>
        <span className="session-title">
          <strong>{session.agent_nickname || shortID(sid) || "session"}</strong>
          <small>{formatAge(session.last_event_age_seconds)} · {session.process_count ?? 0} pid</small>
        </span>
      </button>
      <span className="session-tool-pair">
        <ToolIcon tool={session.tool || "unknown"} />
        {host ? <HostAppButton t={t} host={host} /> : <span className="host-empty" title={t("host")}>{compact ? "" : t("host")}</span>}
      </span>
      <button className="mini-icon" type="button" title={t("copySession")} onClick={() => copyText(sid)}>
        <Copy size={13} />
      </button>
    </div>
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
        <Readout label="ID" value={session.session_id || "n/a"} />
        <Readout label={t("tools")} value={toolDisplayName(session.tool)} />
        <Readout label={t("host")} value={(session.host_apps ?? []).map((app) => app.name).join(", ") || "n/a"} />
        <Readout label={t("command")} value={session.path || "n/a"} />
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
        <span>pid {process.pid ?? "n/a"}</span>
      </div>
      <div className="entity-grid">
        <Readout label={t("metricMatched")} value={String(process.mapped_sessions ?? 0)} />
        <Readout label={t("host")} value={process.host_app?.name || "n/a"} />
        <Readout label={t("command")} value={process.command || "n/a"} />
      </div>
    </section>
  );
}

function Readout({ label, value }: { label: string; value?: string }) {
  return (
    <span className="readout">
      <b>{label}</b>
      <strong>{value || "n/a"}</strong>
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
    <button className="host-app" type="button" title={`${t("openHost")}: ${host.name || host.pid}`} onClick={() => openHostApp(host)}>
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

function LanguageControl({ lang, setLang }: { lang: Lang; setLang: (lang: Lang) => void }) {
  return (
    <div className="lang-control" aria-label="Language">
      <Languages size={14} />
      {(["en", "zh", "ja"] as Lang[]).map((item) => (
        <button key={item} className={lang === item ? "is-active" : ""} type="button" onClick={() => setLang(item)}>
          {item.toUpperCase()}
        </button>
      ))}
    </div>
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

function resolveSelection(snapshot: Snapshot | null, selection: Selection): SelectedView {
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
      title: project?.project || "Unassigned",
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
      title: session?.project || shortID(session?.session_id) || "Session",
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
    title: `${process?.tool || "process"} · ${process?.pid ?? "pid"}`,
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

function buildRailItems(snapshot: Snapshot | null, tab: RailTab, query: string): RailItem[] {
  if (!snapshot) return [];
  const needle = query.trim().toLowerCase();
  const filter = (item: RailItem) => !needle || `${item.title} ${item.description} ${item.command} ${item.tags.join(" ")}`.toLowerCase().includes(needle);
  let items: RailItem[];
  if (tab === "projects") {
    items = (snapshot.project_focus ?? []).map((project) => ({
      id: safeID(project.project),
      type: "project",
      kind: "scan",
      title: project.project || "Unassigned",
      description: `${project.session_count ?? 0} sessions · ${project.process_count ?? 0} processes`,
      command: `attention ${formatPct(project.attention_share_pct)}`,
      status: (project.active_burst_count ?? 0) > 0 ? "active" : "done",
      tags: [`main ${project.main_agent_sessions ?? 0}`, `sub ${project.subagent_sessions ?? 0}`],
      value: `${project.active_burst_count ?? 0} fresh`,
    }));
  } else if (tab === "sessions") {
    items = (snapshot.live_sessions ?? []).map((session) => ({
      id: safeID(session.session_id),
      type: "session",
      kind: "query",
      title: session.project || shortID(session.session_id) || "Session",
      description: `${session.tool || "tool"} · ${session.session_role || "unknown"} · ${session.freshness || "unknown"}`,
      command: session.path || shortID(session.session_id) || "session",
      status: session.active_burst ? "active" : "done",
      tags: [session.tool || "tool", session.session_role || "unknown"],
      value: formatAge(session.last_event_age_seconds),
    }));
  } else {
    items = (snapshot.live_processes ?? []).map((process) => ({
      id: String(process.pid ?? ""),
      type: "process",
      kind: "verify",
      title: `${process.tool || "tool"} · ${process.pid ?? "pid"}`,
      description: `${process.mapped_sessions ?? 0} mapped sessions`,
      command: process.command || "process",
      status: (process.mapped_sessions ?? 0) > 0 ? "done" : "failed",
      tags: [process.tool || "tool", process.host_app?.name || "host unknown"],
      value: `${process.mapped_sessions ?? 0} mapped`,
    }));
  }
  return items.filter(filter);
}

function selectionMetrics(t: (key: string) => string, snapshot: Snapshot, selected: SelectedView) {
  const stats = snapshot.transcript_stats;
  const risk = snapshot.coordination_risk;
  return [
    { key: t("resultKind"), value: selected.kind, cls: "is-accent", icon: <Terminal size={15} /> },
    { key: t("source"), value: stats?.cached ? "cache" : "fresh", cls: "", icon: <Activity size={15} /> },
    { key: t("samples"), value: snapshot.history?.retained_sample_count ?? 0, cls: "", icon: <Gauge size={15} /> },
    { key: t("topProject"), value: risk?.top_project || "none", cls: "", icon: <GitBranch size={15} /> },
  ];
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

function groupSessionsByTool(sessions: LiveSession[]): Array<{ tool: string; sessions: LiveSession[] }> {
  const groups = new Map<string, LiveSession[]>();
  sessions.forEach((session) => {
    const tool = String(session.tool || "unknown").trim() || "unknown";
    groups.set(tool, [...(groups.get(tool) ?? []), session]);
  });
  return Array.from(groups.entries())
    .map(([tool, items]) => ({ tool, sessions: items }))
    .sort((a, b) => {
      const activeDelta = b.sessions.filter((session) => session.active_burst).length - a.sessions.filter((session) => session.active_burst).length;
      if (activeDelta) return activeDelta;
      return a.tool.localeCompare(b.tool);
    });
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

function roleGlyph(role: "main" | "subagent" | "unknown"): string {
  if (role === "main") return "M";
  if (role === "subagent") return "S";
  return "?";
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
    if (!notes.length) return `${t("evidence")}: none`;
    return notes.map((line, index) => `${String(index + 1).padStart(2, "0")}  ${line}`).join("\n");
  }
  const history = bestWindow(snapshot.trends);
  const runtime = bestWindow(snapshot.realtime_trends);
  const points = mergeTrendPoints(history?.points ?? [], runtime?.points ?? []).slice(-18);
  return points
    .map((point) => {
      const at = point.at ? formatDateTime(point.at) : "n/a";
      return `${at}  fresh=${point.active_burst_concurrency ?? "-"} sessions=${point.session_concurrency ?? "-"} pids=${point.pid_concurrency ?? "-"} mapped=${point.mapped_processes ?? "-"}`;
    })
    .join("\n") || `${t("trend")}: empty`;
}

function postHostAction(action: "open_dashboard" | "close" | "quit") {
  if (window.webkit?.messageHandlers?.agentLoadAction) {
    window.webkit.messageHandlers.agentLoadAction.postMessage({ action });
    return;
  }
  if (action === "open_dashboard") window.open("/dashboard", "_blank", "noopener,noreferrer");
  if (action === "close") window.close();
}

function initialLang(): Lang {
  const stored = window.localStorage.getItem("agentload.lang");
  return stored === "zh" || stored === "ja" || stored === "en" ? stored : "en";
}

function initialTheme(): Theme {
  return window.localStorage.getItem("agentload.theme") === "light" ? "light" : "dark";
}

function initialRefreshInterval(): number {
  const raw = Number(window.localStorage.getItem(REFRESH_INTERVAL_STORAGE_KEY));
  return REFRESH_INTERVALS_MS.includes(raw as (typeof REFRESH_INTERVALS_MS)[number]) ? raw : 300_000;
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

function formatPct(value?: number): string {
  if (typeof value !== "number" || Number.isNaN(value)) return "0%";
  return `${Math.round(value)}%`;
}

function formatDateTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function formatAge(seconds?: number): string {
  if (typeof seconds !== "number" || seconds < 0) return "n/a";
  if (seconds < 60) return `${Math.round(seconds)}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h`;
  return `${Math.round(seconds / 86400)}d`;
}

function formatRefreshInterval(ms: number, t: (key: string) => string): string {
  if (!ms) return t("idle");
  return formatAge(ms / 1000);
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
