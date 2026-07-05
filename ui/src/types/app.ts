import type { LiveSession } from "./snapshot";

export type Theme = "dark" | "light";
export type RailTab = "projects" | "sessions" | "processes";
export type LogTab = "summary" | "evidence" | "trend";
export type PopoverView = "online" | "trend" | "system";

export type Selection =
  | { type: "overview"; id: "overview" }
  | { type: "project"; id: string }
  | { type: "session"; id: string }
  | { type: "process"; id: string };

export type RefreshReason = "initial" | "manual" | "auto";

export type ActiveElementIdentity =
  | { type: "focus-key"; key: string }
  | { type: "selector"; selector: string };

export type ViewportState = {
  windowX: number;
  windowY: number;
  activeElement: HTMLElement | null;
  activeIdentity: ActiveElementIdentity | null;
  scrollTargets: Array<{ selector: string; index: number; top: number; left: number }>;
};

export type RailItem = {
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

export type SessionBranch = { parent: LiveSession; children: LiveSession[] };

export type ToolSessionGroup = {
  tool: string;
  sessions: LiveSession[];
  activeCount: number;
  linked: SessionBranch[];
  unlinked: LiveSession[];
  unknown: LiveSession[];
};

export type RoleCounts = {
  main: number;
  sub: number;
  unknown: number;
  total: number;
  activeMain: number;
  activeSub: number;
  activeUnknown: number;
  activeTotal: number;
};

export type ProjectMetricScope = "active" | "all";
export type ProjectMetricObject = "main" | "subagent" | "total";

export type SelectedView = {
  title: string;
  kind: "scan" | "query" | "verify";
  status: "queued" | "running" | "done" | "failed" | "canceled" | "empty";
  command: string;
  summary: Record<string, string | number>;
  details: string[];
};
