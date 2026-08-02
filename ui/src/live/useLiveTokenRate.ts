import { useEffect, useState } from "react";
import type { LiveTokenRateSample } from "../types/snapshot";

const LIVE_TOKEN_RATE_POLL_MS = 30_000;

export function useLiveTokenRate(active: boolean): LiveTokenRateSample | undefined {
  const [sample, setSample] = useState<LiveTokenRateSample>();

  useEffect(() => {
    if (!active) return;
    let cancelled = false;
    let timer = 0;
    const poll = async () => {
      try {
        const response = await fetch("/api/live-token-rate", { cache: "no-store" });
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const next = (await response.json()) as LiveTokenRateSample;
        if (!cancelled) setSample(next);
      } catch {
        if (!cancelled) {
          setSample({
            output_tokens_per_second: null,
            state: "unavailable",
            basis: "output_tokens",
            window_seconds: 180,
            sample_interval_seconds: 30,
          });
        }
      } finally {
        if (!cancelled) timer = window.setTimeout(poll, LIVE_TOKEN_RATE_POLL_MS);
      }
    };
    void poll();
    return () => {
      cancelled = true;
      if (timer) window.clearTimeout(timer);
    };
  }, [active]);

  return sample;
}
