import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { Activity, ArrowUpRight, Bot, Gauge, GitBranch, Languages, Moon, RefreshCw, Search, Server, Sun, Terminal, X } from "lucide-react";
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
  transcript_stats?: { scanned_files?: number; parsed_files?: number; cached?: boolean; errors?: string[] };
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

type ProjectSnapshot = {
  project?: string;
  session_count?: number;
  active_burst_count?: number;
  main_agent_sessions?: number;
  subagent_sessions?: number;
  unknown_role_sessions?: number;
  process_count?: number;
  attention_share_pct?: number;
  stale_session_count?: number;
  recent_session_count?: number;
  confidence?: string;
  last_event_age_seconds?: number;
  tools?: Array<{ tool?: string; session_count?: number; active_burst_count?: number; process_count?: number }>;
};

type LiveProcess = {
  pid?: number;
  tool?: string;
  command?: string;
  mapped_sessions?: number;
  session_ids?: string[];
  host_app?: { pid?: number; name?: string; bundle_path?: string };
};

type LiveSession = {
  tool?: string;
  session_id?: string;
  session_role?: string;
  role_confidence?: string;
  project?: string;
  process_count?: number;
  active_burst?: boolean;
  freshness?: string;
  mapping_method?: string;
  confidence?: string;
  last_event_age_seconds?: number;
  path?: string;
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

  const t = useCallback((key: string) => copy[lang][key] || copy.en[key] || key, [lang]);
  const fetchSnapshot = useCallback(async () => {
    const response = await fetch("/api/snapshot", { cache: "no-store" });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const next = (await response.json()) as Snapshot;
    setSnapshot(next);
    setError(null);
  }, []);
  const refreshSnapshot = useCallback(async () => {
    setRefreshing(true);
    try {
      await fetch("/api/refresh", { method: "POST", headers: { "Content-Type": "application/json" } });
      await fetchSnapshot();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRefreshing(false);
    }
  }, [fetchSnapshot]);

  useEffect(() => {
    void fetchSnapshot().catch((err) => setError(err instanceof Error ? err.message : String(err)));
  }, [fetchSnapshot]);
  useEffect(() => {
    if (!refreshInterval) return;
    const id = window.setInterval(() => {
      if (document.visibilityState === "visible") void fetchSnapshot().catch(() => undefined);
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
          <svg viewBox="0 0 24 24" width="22" height="22" fill="none">
            <path d="M4 19V5l8 5 8-5v14" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
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
