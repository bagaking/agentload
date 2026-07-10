import { useEffect, useState } from "react";
import type { SystemResourceSnapshot } from "../types/snapshot";

export type SystemResourceHistoryPoint = Pick<SystemResourceSnapshot,
  "sampled_at" | "cpu_percent" | "memory_used_pct" | "disk_used_pct" |
  "network_rx_bytes_per_sec" | "network_tx_bytes_per_sec" | "thermal_state">;

const MAX_HISTORY_POINTS = 30;

function toHistoryPoint(resources: SystemResourceSnapshot): SystemResourceHistoryPoint {
  return {
    sampled_at: resources.sampled_at,
    cpu_percent: resources.cpu_percent,
    memory_used_pct: resources.memory_used_pct,
    disk_used_pct: resources.disk_used_pct,
    network_rx_bytes_per_sec: resources.network_rx_bytes_per_sec,
    network_tx_bytes_per_sec: resources.network_tx_bytes_per_sec,
    thermal_state: resources.thermal_state,
  };
}

export function useLiveSystemResources(initial: SystemResourceSnapshot | undefined, active: boolean) {
  const [resources, setResources] = useState<SystemResourceSnapshot | undefined>(initial);
  const [history, setHistory] = useState<SystemResourceHistoryPoint[]>([]);

  useEffect(() => {
    setResources(initial);
  }, [initial?.sampled_at]);

  useEffect(() => {
    if (!resources?.sampled_at) return;
    const sample = toHistoryPoint(resources);
    setHistory((current) => {
      if (current[current.length - 1]?.sampled_at === sample.sampled_at) return current;
      return [...current, sample].slice(-MAX_HISTORY_POINTS);
    });
  }, [resources]);

  useEffect(() => {
    if (!active) return;
    let cancelled = false;
    let timer = 0;
    const poll = async () => {
      try {
        const response = await fetch("/api/system-resources", { cache: "no-store" });
        if (response.ok) {
          const next = (await response.json()) as SystemResourceSnapshot;
          if (!cancelled) setResources(next);
        }
      } catch {
        // Keep the last good sample; the snapshot refresh path will surface broader failures.
      } finally {
        if (!cancelled) timer = window.setTimeout(poll, 2000);
      }
    };
    void poll();
    return () => {
      cancelled = true;
      if (timer) window.clearTimeout(timer);
    };
  }, [active]);

  return { resources, history };
}
