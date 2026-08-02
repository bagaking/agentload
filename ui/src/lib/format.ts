import type { TokenUsage } from "../types/snapshot";

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

export function formatCPU(value?: number): string {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) return "0.00%";
  if (value < 0.01) return "<0.01%";
  return `${value.toFixed(2)}%`;
}

export function formatCompactCPU(value?: number): string {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) return "0.00%";
  if (value < 0.01) return "<.01%";
  return `${value.toFixed(2)}%`;
}

export function formatMemory(bytes?: number, t?: Translate): string {
  if (typeof bytes !== "number" || !Number.isFinite(bytes) || bytes <= 0) return t ? t("unavailable") : "n/a";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  const digits = value >= 100 || unit === 0 ? 0 : value >= 10 ? 1 : 2;
  return `${value.toFixed(digits)} ${units[unit]}`;
}

export function formatBytesPerSecond(bytes?: number, t?: Translate): string {
  if (typeof bytes !== "number" || !Number.isFinite(bytes) || bytes <= 0) return "0 B/s";
  return `${formatMemory(bytes, t ?? ((key: string) => key))}/s`;
}

export function formatPacketRate(value?: number, t?: Translate): string {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) return `0 ${t?.("packetsShort") ?? "pkt"}/s`;
  const suffix = `${t?.("packetsShort") ?? "pkt"}/s`;
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(value >= 10_000_000 ? 0 : 1)}M ${suffix}`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(value >= 10_000 ? 0 : 1)}K ${suffix}`;
  return `${value.toFixed(value >= 100 ? 0 : value >= 10 ? 1 : 2)} ${suffix}`;
}

export function formatPrecisePct(value?: number): string {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) return "0.00%";
  if (value < 0.01) return "<0.01%";
  return `${value.toFixed(2)}%`;
}

export function formatLoadAverage(value?: number): string {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) return "0.00";
  return value.toFixed(2);
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

export function formatTokenUsageSummary(usage: TokenUsage | undefined, t: Translate): string {
  if (!usage || !tokenUsageHasValue(usage)) return t("unavailable");
  const parts: string[] = [];
  const total = usage.total_tokens ?? tokenUsageDerivedTotal(usage);
  if (total > 0) parts.push(`${formatTokenCount(total)} ${t("tokenTotal")}`);
  if ((usage.input_tokens ?? 0) > 0) parts.push(`${formatTokenCount(usage.input_tokens)} ${t("tokenInput")}`);
  if ((usage.output_tokens ?? 0) > 0) parts.push(`${formatTokenCount(usage.output_tokens)} ${t("tokenOutput")}`);
  const cache = (usage.cache_creation_input_tokens ?? 0) + (usage.cache_read_input_tokens ?? 0);
  if (cache > 0) parts.push(`${formatTokenCount(cache)} ${t("tokenCache")}`);
  if ((usage.reasoning_output_tokens ?? 0) > 0) parts.push(`${formatTokenCount(usage.reasoning_output_tokens)} ${t("tokenReasoning")}`);
  return parts.join(" · ") || t("unavailable");
}

export function formatTokenCount(value?: number): string {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) return "0";
  return new Intl.NumberFormat(undefined, {
    notation: value >= 10000 ? "compact" : "standard",
    maximumFractionDigits: value >= 10000 ? 1 : 0,
  }).format(value);
}

export function formatTokenRate(value?: number | null): string {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) return "—";
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(value >= 10_000_000 ? 0 : 1)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(value >= 10_000 ? 0 : 1)}K`;
  if (value >= 100) return value.toFixed(0);
  if (value >= 10) return value.toFixed(1);
  return value.toFixed(value === 0 ? 0 : 2);
}

export function tokenUsageDerivedTotal(usage: TokenUsage): number {
  return (usage.input_tokens ?? 0) + (usage.output_tokens ?? 0) + (usage.cache_creation_input_tokens ?? 0) + (usage.cache_read_input_tokens ?? 0);
}

export function tokenUsageHasValue(usage: TokenUsage): boolean {
  return tokenUsageDerivedTotal(usage) > 0 || (usage.total_tokens ?? 0) > 0 || (usage.reasoning_output_tokens ?? 0) > 0;
}

export function shortID(value?: string): string {
  if (!value) return "";
  return value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-6)}` : value;
}

export function safeID(value?: string): string {
  const text = String(value || "unassigned").trim();
  return text || "unassigned";
}
