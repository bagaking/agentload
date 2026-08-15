import React, { useEffect, useState } from "react";
import { Cpu } from "lucide-react";
import { formatCPU, formatMemory, type Translate } from "../lib/format";
import type { SelfResourceSnapshot } from "../types/snapshot";

export function SelfResources({ t, active }: { t: Translate; active: boolean }) {
  const [sample, setSample] = useState<SelfResourceSnapshot>();
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    if (!active) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    let request: AbortController | undefined;
    const read = async () => {
      if (!document.hidden) {
        request = new AbortController();
        const timeout = setTimeout(() => request?.abort(), 3000);
        try {
          const response = await fetch("/api/self-resources", { cache: "no-store", signal: request.signal });
          if (!response.ok) throw new Error(String(response.status));
          const value = await response.json() as SelfResourceSnapshot;
          if (!stopped) { setSample(value); setFailed(false); }
        } catch { if (!stopped) setFailed(true); }
        finally { clearTimeout(timeout); }
      }
      if (!stopped) timer = setTimeout(read, 2000);
    };
    void read();
    return () => { stopped = true; clearTimeout(timer); request?.abort(); };
  }, [active]);
  const bytes = (value: number | null | undefined) => value == null ? "—" : value === 0 ? "0 B" : formatMemory(value);
  const time = (value?: string) => value ? new Date(value).toLocaleTimeString([], { hour12: false }) : "—";
  const components = sample?.storage.components ?? [];
  const labels: Record<string, string> = {
    trajectory: t("selfComponent_trajectory"), throughput: t("selfComponent_throughput"),
    history: t("selfComponent_history"), other: t("selfComponent_other"), self_monitor: t("selfComponent_self_monitor"),
  };
  return <section className="diagnostic-self" aria-label={t("selfResourcesTitle")}>
    <div className="diagnostic-plane-head"><span><Cpu size={15} />Agent Load · {t("selfResourcesTitle")}</span><em>{time(sample?.sampled_at)}</em></div>
    <div className="self-metrics">
      <div><span>CPU</span><strong>{sample?.cpu_percent == null ? "—" : formatCPU(sample.cpu_percent)}</strong><small>{sample?.cpu_window_seconds ? `${sample.cpu_window_seconds.toFixed(1)}s · ${t("selfOneCore")}` : t("selfFirstSample")}</small></div>
      <div><span>{t("selfMemory")}</span><strong>{bytes(sample?.rss_bytes)}</strong><small>{t("selfProcessOnly")}</small></div>
      <div><span>{t("selfDisk")}</span><strong>{bytes(sample?.storage.allocated_bytes)}</strong><small>{t("selfAllocated")}</small></div>
      <div><span>{t("selfAvailable")}</span><strong>{bytes(sample?.storage.available_bytes)}</strong><small>{t("selfDataVolume")}</small></div>
    </div>
    {failed && <p className="self-gap" role="status">{t("selfReadFailed")}</p>}
    {sample && !sample.storage.complete && <p className="self-gap" role="status">{t("selfPartial")}</p>}
    <details className="self-breakdown">
      <summary>{t("selfBreakdown")}<span>{time(sample?.storage.sampled_at)}</span></summary>
      <p className="self-note">{t("selfStorageScope")}</p>
      {[...components].sort((a,b) => (b.allocated_bytes ?? 0)-(a.allocated_bytes ?? 0)).map(component => <details key={component.key} className="self-component">
        <summary><span>{labels[component.key] ?? labels.other}</span><b>{bytes(component.allocated_bytes)}</b></summary>
        {component.key === "self_monitor" ? <p className="self-note">{t("selfMonitorCost")} · {sample?.monitor.sample_ms.toFixed(2)} ms · {t("selfNoStorage")}</p> : <>
          <div className="self-file self-file-head"><span>{t("selfFiles")}</span><span>{t("selfAllocated")}</span><span>{t("selfLogical")}</span></div>
          {component.files.map(file => <div key={file.name} className="self-file"><span title={file.name}>{file.name}</span><span>{bytes(file.allocated_bytes)}</span><span>{bytes(file.logical_bytes)}</span></div>)}
          {!component.files.length && <p className="self-note">{component.complete ? t("selfNoStorage") : t("selfPartial")}</p>}
        </>}
      </details>)}
      <p className="self-note">{t("selfSharedResources")}</p>
    </details>
  </section>;
}
