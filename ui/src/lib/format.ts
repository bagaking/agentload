export type Translate = (key: string) => string;

export function pctPart(value: number | undefined, total: number): number {
  if (!value || total <= 0) return 0;
  return (value / total) * 100;
}

export function clampPct(value: number, min = 0): number {
  if (!Number.isFinite(value)) return min;
  return Math.max(min, Math.min(100, value));
}

export function clampNumber(value: number, min: number, max: number): number {
  if (!Number.isFinite(value)) return min;
  return Math.max(min, Math.min(max, value));
}

export function formatPct(value?: number): string {
  if (typeof value !== "number" || Number.isNaN(value)) return "0%";
  return `${Math.round(value)}%`;
}

export function formatDateTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function formatChartAxisLabel(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return `${date.toLocaleDateString([], { month: "numeric", day: "numeric" })} ${date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
}

export function formatAge(seconds?: number, t?: Translate): string {
  if (typeof seconds !== "number" || seconds < 0) return t ? t("unavailable") : "n/a";
  if (seconds < 60) return `${Math.round(seconds)}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h`;
  return `${Math.round(seconds / 86400)}d`;
}

export function formatRefreshInterval(ms: number, t: Translate): string {
  if (!ms) return t("refreshPaused");
  return formatAge(ms / 1000, t);
}

export function countLabel(t: Translate, key: string, count: number): string {
  return t(key).replace("{count}", String(count));
}

export function formatCopy(template: string, values: Record<string, string | number>): string {
  return Object.entries(values).reduce((text, [key, value]) => text.split(`{${key}}`).join(String(value)), template);
}

export function shortID(value?: string): string {
  if (!value) return "";
  return value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value;
}

export function safeID(value?: string): string {
  const text = String(value || "unassigned").trim();
  return text || "unassigned";
}

export function compactCommand(value?: string): string {
  const text = String(value || "").trim();
  if (text.length <= 96) return text;
  return `${text.slice(0, 92)}...`;
}
