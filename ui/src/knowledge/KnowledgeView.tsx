import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { ArrowLeft, Search, X, ChevronRight } from "lucide-react";
import { accessPreference, querySelector, trajectoryRPC, TrajectoryError, type Access, type AttentionResult, type ContextManifest, type Coverage, type Entity, type Knowledge, type QueryResult, type RelationResult, type Selector, type Slice, type TrajectoryContext, type TrajectorySession } from "./trajectoryApi";
import { AttentionDetails, ContextDetails, CoverageNotice, EntityDetails, KnowledgeDetails, SourceButton } from "./EvidenceViews";
import { evidenceLabel } from "./knowledgeLabels";
import { formatCopy } from "../lib/format";
import "./knowledge.css";

const KnowledgeGraph = lazy(async () => ({ default: (await import("./KnowledgeGraph")).KnowledgeGraph }));
const views = ["evidence", "insight", "relations"] as const;
type DetailView = typeof views[number];
const viewKeys = { insight: "knowledgeInsightTab", evidence: "knowledgeProcessTab", relations: "knowledgeRelationsTab" };
type Episode = { id: string; agent: string; title: string; preview?: string; session?: TrajectorySession; eventIDs: string[]; knowledge: Knowledge[]; entities: Entity[]; contexts: TrajectoryContext[] };
type RawWindow = { slice: Slice; text: string; clipped: boolean };

function episodesFrom(result: QueryResult): Episode[] {
  const groups = new Map<string, Episode>();
  const group = (id: string, agent = "") => {
    let item = groups.get(id);
    if (!item) { item = { id, agent, title: id, eventIDs: [], knowledge: [], entities: [], contexts: [] }; groups.set(id, item); }
    if (agent) item.agent = agent;
    return item;
  };
  const reference = (item: Episode, id: string) => { if (id && !item.eventIDs.includes(id)) item.eventIDs.push(id); };
  for (const session of result.sessions ?? []) { const item = group(session.id, session.agent); item.session = session; item.title = session.title; item.preview = session.matched_preview; session.matched_ids.forEach(id => reference(item, id)); }
  for (const event of result.events ?? []) { const item = group(event.session_id); reference(item, event.id); if (!item.preview) item.preview = event.text; }
  for (const record of result.knowledge ?? []) for (const source of record.sources) { const item = group(source.session_id, source.agent); reference(item, source.event_id); if (!item.preview) item.preview = record.text; if (!item.knowledge.some(other => other.id === record.id)) item.knowledge.push(record); }
  for (const entity of result.entities ?? []) for (const occurrence of entity.occurrences) { const item = group(occurrence.session_id); reference(item, occurrence.event_id); if (!item.preview) item.preview = entity.literal; if (!item.entities.some(other => other.id === entity.id)) item.entities.push(entity); }
  for (const context of result.contexts ?? []) { const item = group(context.session_id); item.contexts.push(context); if (context.anchor_id) reference(item, context.anchor_id); }
  for (const attention of result.attention ?? []) { const item = group(attention.session_id, attention.agent); for (const intervention of attention.interventions) intervention.evidence.forEach(ref => reference(item, ref.event_id)); }
  for (const actor of result.actors ?? []) reference(group(actor.session_id), actor.event_id);
  for (const node of result.nodes ?? []) if (node.type === "event") reference(group(node.session_id), node.id);
  return [...groups.values()];
}

function plainQueryWords(query: string): string[] {
  try { return [...new Set((querySelector(query).text ?? "").split(/\s+/).filter(Boolean))]; } catch { return []; }
}

function HighlightedText({ text, words }: { text: string; words: string[] }) {
  if (!words.length) return text;
  const escaped = [...words].sort((a, b) => b.length - a.length).map(word => word.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  return text.split(new RegExp(`(${escaped.join("|")})`, "giu")).map((part, index) => index % 2 ? <mark key={index}>{part}</mark> : part);
}

function mergePage(previous: QueryResult, next: QueryResult): QueryResult {
  let dropped = 0;
  const unique = <T extends { id: string }>(a: T[] = [], b: T[] = []) => { const items = [...new Map([...a, ...b].map(item => [item.id, item])).values()]; dropped += Math.max(0, items.length - 200); return items.slice(-200); };
  const value = { ...next, sessions: unique(previous.sessions, next.sessions), events: unique(previous.events, next.events), entities: unique(previous.entities, next.entities), knowledge: unique(previous.knowledge, next.knowledge), contexts: unique(previous.contexts, next.contexts), attention: unique(previous.attention, next.attention), actors: unique(previous.actors, next.actors), nodes: unique(previous.nodes, next.nodes), relations: unique(previous.relations, next.relations) };
  value.coverage = { ...next.coverage, complete: next.coverage.complete && !dropped, gaps: [...new Set([...next.coverage.gaps, ...(dropped ? ["ui_result_window_limit"] : [])])], omitted: Math.max(previous.coverage.omitted, next.coverage.omitted) + dropped };
  return value;
}

export function KnowledgeView({ t, surfaceVisible }: { t: (key: string) => string; surfaceVisible: boolean }) {
  const [access, setAccess] = useState<Access | null>(null);
  const [query, setQuery] = useState("");
  const [result, setResult] = useState<QueryResult | null>(null);
  const [episodes, setEpisodes] = useState<Episode[]>([]);
  const [episode, setEpisode] = useState<Episode | null>(null);
  const [selectedID, setSelectedID] = useState("");
  const [slice, setSlice] = useState<Slice | null>(null);
  const [record, setRecord] = useState<Knowledge | null>(null);
  const [entity, setEntity] = useState<Entity | null>(null);
  const [notes, setNotes] = useState<Knowledge[]>([]);
  const [notesCoverage, setNotesCoverage] = useState<Coverage>();
  const [notesNext, setNotesNext] = useState("");
  const [contexts, setContexts] = useState<TrajectoryContext[]>([]);
  const [contextScope, setContextScope] = useState("actual_input");
  const [contextCoverage, setContextCoverage] = useState<Coverage>();
  const [contextNext, setContextNext] = useState("");
  const [manifest, setManifest] = useState<ContextManifest | null>(null);
  const [contextSelection, setContextSelection] = useState<{ id: string; offset: number } | null>(null);
  const [attention, setAttention] = useState<AttentionResult | null>(null);
  const [graph, setGraph] = useState<RelationResult | null>(null);
  const [graphMounted, setGraphMounted] = useState(false);
  const [raw, setRaw] = useState<RawWindow | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<Record<string, boolean>>({ access: true });
  const [view, setView] = useState<DetailView>("evidence");
  const [refs, setRefs] = useState(false);
  const [revision, setRevision] = useState(0);
  const [queryRefresh, setQueryRefresh] = useState(0);
  const search = useRef<HTMLInputElement>(null);
  const requests = useRef(new Map<string, AbortController>());
  const generation = useRef(0);
  const baseFocus = useRef("");
  const decoder = useRef<TextDecoder | null>(null);
  const indexPending = Boolean(result?.coverage.gaps.includes("index_pending"));
  const searchIndexPending = Boolean(result?.coverage.gaps.includes("search_index_pending"));
  const pending = Object.values(busy).some(Boolean) || Boolean(access?.enabled && access.authorized && !result && !error);
  const setPending = (key: string, value: boolean) => setBusy(previous => ({ ...previous, [key]: value }));
  const start = (key: string) => { requests.current.get(key)?.abort(); const controller = new AbortController(); requests.current.set(key, controller); setPending(key, true); return controller; };
  const failed = (err: unknown) => setError(err instanceof TrajectoryError ? err.code === -32002 || err.code === -32004 ? "source_stale" : err.code === -32602 ? "invalid_query" : err.code === -32009 ? "storage_low" : err.code === -32003 || err.code === 403 ? "access_denied" : err.message : String(err));
  const abortDetail = () => { for (const [key, controller] of requests.current) if (key !== "query" && key !== "access") controller.abort(); setBusy(previous => ({ query: previous.query, access: previous.access })); };

  useEffect(() => () => { for (const controller of requests.current.values()) controller.abort(); }, []);

  useEffect(() => {
    if (!surfaceVisible) { for (const controller of requests.current.values()) controller.abort(); setBusy({}); return; }
    const controller = start("access");
    void accessPreference(undefined, controller.signal).then(value => {
      if (controller.signal.aborted) return;
      setAccess(value);
      if (!value.enabled || !value.authorized) { setEpisode(null); setResult(null); setEpisodes([]); setSlice(null); setRecord(null); setEntity(null); setNotes([]); setGraph(null); setContexts([]); setAttention(null); setManifest(null); setContextSelection(null); setRaw(null); }
    }).catch(err => { if (!controller.signal.aborted) failed(err); }).finally(() => { if (!controller.signal.aborted) setPending("access", false); });
    return () => controller.abort();
  }, [surfaceVisible]);

  const hydrate = async (value: QueryResult, signal: AbortSignal) => {
    const groups = episodesFrom(value);
    // Only current page source groups are hydrated. The service owns matching
    // counts; an event-reference projection never becomes a synthetic total.
    const missing = groups.filter(item => !item.session).slice(0, 20);
    for (let i = 0; i < missing.length && !signal.aborted; i += 4) {
      await Promise.all(missing.slice(i, i + 4).map(async item => {
        const metadata = await trajectoryRPC<QueryResult>("traj.query", { collection: "sessions", session_id: item.id, limit: 1 }, signal);
        const session = metadata.sessions?.[0];
        if (session) { item.session = session; item.title = session.title; item.agent = session.agent; }
      }));
    }
    return groups;
  };

  useEffect(() => {
    if (episode) { setPending("query", false); return; }
    if (!surfaceVisible || busy.access || !access?.enabled || !access.authorized) return;
    const current = ++generation.current;
    const controller = start("query");
    setError("");
    const timer = window.setTimeout(() => {
      let selector: Selector;
      try { selector = querySelector(query); } catch { setError("invalid_query"); setResult(null); setEpisodes([]); setPending("query", false); return; }
      void trajectoryRPC<QueryResult>("traj.query", selector, controller.signal).then(async value => {
        const groups = await hydrate(value, controller.signal);
        if (!controller.signal.aborted && generation.current === current) { setResult(value); setEpisodes(groups); }
      }).catch(err => { if (!controller.signal.aborted && generation.current === current) { failed(err); setResult(null); setEpisodes([]); } }).finally(() => { if (!controller.signal.aborted && generation.current === current) setPending("query", false); });
    }, query ? 180 : 0);
    return () => { clearTimeout(timer); controller.abort(); };
  }, [access?.enabled, access?.authorized, busy.access, query, revision, queryRefresh, surfaceVisible, episode?.id]);

  useEffect(() => {
    if (episode || !surfaceVisible || !access?.enabled || !access.authorized || busy.query || !indexPending) return;
    const timer = window.setTimeout(() => setQueryRefresh(value => value + 1), 1000);
    return () => clearTimeout(timer);
  }, [surfaceVisible, access?.enabled, access?.authorized, busy.query, indexPending, query, episode?.id]);

  useEffect(() => {
    if (!surfaceVisible || !episode || (view !== "insight" && view !== "relations")) return;
    const controller = start("details");
    void Promise.allSettled([
      trajectoryRPC<QueryResult>("traj.query", { collection: "knowledge", session_id: episode.id, limit: 20 }, controller.signal),
      trajectoryRPC<AttentionResult>("traj.get", { id: episode.id, view: "attention", around: 0, max_bytes: 5120 }, controller.signal),
    ]).then(values => {
      if (controller.signal.aborted) return;
      const knowledge = values[0], progress = values[1];
      if (knowledge.status === "fulfilled") { const value = knowledge.value as QueryResult; setNotes(value.knowledge ?? []); setNotesCoverage(value.coverage); setNotesNext(value.next ?? ""); }
      else failed(knowledge.reason);
      if (progress.status === "fulfilled") setAttention(progress.value as AttentionResult); else failed(progress.reason);
    }).finally(() => { if (!controller.signal.aborted) setPending("details", false); });
    return () => { controller.abort(); setPending("details", false); };
  }, [episode?.id, surfaceVisible, view, revision]);

  useEffect(() => {
    if (!surfaceVisible || !episode || view !== "insight") return;
    const controller = start("contexts");
    setManifest(previous => previous?.context?.scope === contextScope && previous.context.session_id === episode.id ? previous : null);
    void trajectoryRPC<QueryResult>("traj.query", { collection: "contexts", session_id: episode.id, context_scope: contextScope, limit: 20 }, controller.signal).then(value => { if (!controller.signal.aborted) { setContexts(value.contexts ?? []); setContextCoverage(value.coverage); setContextNext(value.next ?? ""); } }).catch(err => { if (!controller.signal.aborted) failed(err); }).finally(() => { if (!controller.signal.aborted) setPending("contexts", false); });
    return () => { controller.abort(); setPending("contexts", false); };
  }, [episode?.id, contextScope, surfaceVisible, view, revision]);

  useEffect(() => {
    if (!surfaceVisible || !episode || !contextSelection || view !== "insight" || busy.access || !access?.enabled || !access.authorized) return;
    const controller = start("manifest");
    void trajectoryRPC<ContextManifest>("traj.get", { id: contextSelection.id, view: "context", around: 0, member_offset: contextSelection.offset, max_bytes: 5120 }, controller.signal).then(value => { if (!controller.signal.aborted) setManifest(value); }).catch(err => { if (!controller.signal.aborted) { setManifest(null); failed(err); } }).finally(() => { if (!controller.signal.aborted) setPending("manifest", false); });
    return () => { controller.abort(); setPending("manifest", false); };
  }, [contextSelection, episode?.id, contextScope, surfaceVisible, view, access?.enabled, access?.authorized, busy.access, revision]);

  useEffect(() => {
    if (!surfaceVisible || !episode || view !== "relations" || graph) return;
    const controller = start("graph");
    void trajectoryRPC<RelationResult>("traj.get", { id: baseFocus.current, view: "relations", around: 3, max_bytes: 5120 }, controller.signal).then(value => { if (!controller.signal.aborted) { setPending("graph", false); setGraph(value); } }).catch(err => { if (!controller.signal.aborted) failed(err); }).finally(() => { if (!controller.signal.aborted) setPending("graph", false); });
    return () => { controller.abort(); setPending("graph", false); };
  }, [view, episode?.id, surfaceVisible, graph]);

  const changeQuery = (value: string) => { if (value !== query) { generation.current++; requests.current.get("query")?.abort(); setResult(null); setEpisodes([]); setQuery(value); } };
  const setEnabled = async (enabled: boolean) => {
    const controller = start("access"); setError("");
    try { const value = await accessPreference(enabled, controller.signal); if (!controller.signal.aborted) { setAccess(value); setEpisode(null); setSlice(null); setRecord(null); setEntity(null); setNotes([]); setGraph(null); setContexts([]); setAttention(null); setManifest(null); setContextSelection(null); setRaw(null); setResult(null); setEpisodes([]); } }
    catch (err) { if (!controller.signal.aborted) failed(err); }
    finally { if (!controller.signal.aborted) setPending("access", false); }
  };
  const readEvidence = async (id: string) => {
    const controller = start("read"); setError(""); setRaw(null); decoder.current = null;
    try { const value = await trajectoryRPC<Slice>("traj.get", { id, around: 3, max_bytes: 5120 }, controller.signal); if (!controller.signal.aborted) setSlice(value); }
    catch (err) { if (!controller.signal.aborted) { setSlice(null); failed(err); } }
    finally { if (!controller.signal.aborted) setPending("read", false); }
  };
  const pick = async (id: string) => {
    requests.current.get("object")?.abort(); setPending("object", false);
    setRecord(null); setEntity(null);
    setSelectedID(id); setError("");
    if (id.startsWith("e.") || id.startsWith("s.")) { setRecord(null); setEntity(null); await readEvidence(id); return; }
    if (id.startsWith("ctx.")) { await openContext(id); return; }
    const controller = start("object");
    try { const value = await trajectoryRPC<Slice>("traj.get", { id, around: 0, max_bytes: 5120 }, controller.signal); if (!controller.signal.aborted) { setRecord(value.knowledge ?? null); setEntity(value.entity ?? null); } }
    catch (err) { if (!controller.signal.aborted) failed(err); }
    finally { if (!controller.signal.aborted) setPending("object", false); }
  };
  const readSource = (id: string) => { setView("evidence"); void readEvidence(id); };
  const openContext = (id: string, offset = 0) => {
    requests.current.get("manifest")?.abort(); setPending("manifest", false); setError("");
    setManifest(previous => previous?.focus_id === id ? previous : null);
    setContextSelection({ id, offset });
  };
  const changeContextScope = (scope: string) => { requests.current.get("manifest")?.abort(); setPending("manifest", false); setContextSelection(null); setManifest(null); setContexts([]); setContextNext(""); setContextCoverage(undefined); setContextScope(scope); };
  const moreContexts = async () => {
    if (!episode || !contextNext) return;
    const controller = start("contexts");
    try { const value = await trajectoryRPC<QueryResult>("traj.query", { collection: "contexts", session_id: episode.id, context_scope: contextScope, cursor: contextNext, limit: 20 }, controller.signal); if (!controller.signal.aborted) { setContexts(value.contexts ?? []); setContextCoverage(value.coverage); setContextNext(value.next ?? ""); } }
    catch (err) { if (!controller.signal.aborted) failed(err); }
    finally { if (!controller.signal.aborted) setPending("contexts", false); }
  };
  const moreNotes = async () => {
    if (!episode || !notesNext) return;
    const controller = start("notes");
    try { const value = await trajectoryRPC<QueryResult>("traj.query", { collection: "knowledge", session_id: episode.id, cursor: notesNext, limit: 20 }, controller.signal); if (!controller.signal.aborted) { setNotes(value.knowledge ?? []); setNotesCoverage(value.coverage); setNotesNext(value.next ?? ""); } }
    catch (err) { if (!controller.signal.aborted) failed(err); }
    finally { if (!controller.signal.aborted) setPending("notes", false); }
  };
  const readRaw = async (id: string, offset = 0) => {
    const controller = start("raw"); setError("");
    if (offset === 0) { decoder.current = new TextDecoder("utf-8", { fatal: true }); setRaw(null); }
    try {
      const value = await trajectoryRPC<Slice>("traj.get", { id, view: "raw", around: 0, raw_offset: offset, max_bytes: 5120 }, controller.signal);
      if (controller.signal.aborted) return;
      const chunk = value.raw_chunk;
      if (!chunk || chunk.encoding !== "base64" || !decoder.current) throw new Error("invalid_raw_chunk");
      const bytes = Uint8Array.from(atob(chunk.data), character => character.charCodeAt(0));
      const text = decoder.current.decode(bytes, { stream: chunk.next_offset !== undefined });
      setRaw(previous => { const combined = offset ? (previous?.text ?? "") + text : text; const characters = Array.from(combined); return { slice: value, text: characters.slice(-8192).join(""), clipped: Boolean(previous?.clipped || characters.length > 8192) }; });
    } catch (err) { if (!controller.signal.aborted) failed(err); }
    finally { if (!controller.signal.aborted) setPending("raw", false); }
  };
  const open = (item: Episode) => {
    abortDetail(); setEpisode(item); setView("evidence"); setRefs(false); setRecord(null); setEntity(null); setGraph(null); setNotes([]); setContexts([]); setAttention(null); setManifest(null); setContextSelection(null); setRaw(null); setSlice(null); setError("");
    const focus = item.eventIDs[0] || item.id; baseFocus.current = focus;
    setContextScope(item.contexts[0]?.scope ?? "actual_input");
    const selected = item.knowledge[0]?.id || item.entities[0]?.id || item.contexts[0]?.id || focus;
    if (selected === focus) { setSelectedID(selected); void readEvidence(focus); }
    else { void readEvidence(focus); void pick(selected); }
  };
  const back = () => { abortDetail(); setEpisode(null); setSlice(null); setRecord(null); setEntity(null); setGraph(null); setManifest(null); setContextSelection(null); setRaw(null); setError(""); requestAnimationFrame(() => search.current?.focus()); };
  const nextPage = async () => {
    if (!result?.next) return;
    const previous = result, current = generation.current;
    const controller = start("query");
    try { const next = await trajectoryRPC<QueryResult>("traj.query", { ...querySelector(query), cursor: previous.next }, controller.signal); const value = mergePage(previous, next); const groups = await hydrate(value, controller.signal); if (!controller.signal.aborted && current === generation.current) { setResult(value); setEpisodes(groups); } }
    catch (err) { if (!controller.signal.aborted && current === generation.current) failed(err); }
    finally { if (!controller.signal.aborted && current === generation.current) setPending("query", false); }
  };
  const selectedEvent = slice?.events.find(event => event.id === selectedID) ?? slice?.events.find(event => event.id === slice.focus_id);
  const queryWords = plainQueryWords(query);
  const searching = Boolean(access?.enabled && access.authorized && busy.query);
  const badge = <span className="knowledge-sample">{t("knowledgeLocal")}</span>;
  const ready = (access !== null || Boolean(error)) && !pending && (!episode || view !== "relations" || graphMounted || Boolean(error));
  const errorKeys: Record<string, string> = { invalid_query: "knowledgeInvalidQuery", source_stale: "knowledgeSourceStale", access_denied: "knowledgeAccessDenied", storage_low: "knowledgeStorageLow" };
  const inspector = record ? <KnowledgeDetails record={record} onRead={readSource} onPick={id => void pick(id)} t={t} /> : entity ? <EntityDetails entity={entity} onRead={readSource} t={t} /> : <div className="knowledge-inspector recorded"><div className="knowledge-section-heading"><span>{t("knowledgeRecorded")}</span><span>{episode?.agent}</span></div><h3>{selectedEvent?.tool?.name || episode?.session?.last_action || t("knowledgeNoAction")}</h3>{selectedEvent?.text ? <p>{selectedEvent.text}</p> : null}<dl><div><dt>{t("knowledgeScope")}</dt><dd>{episode?.session?.native_id || episode?.id}</dd></div><div><dt>{t("knowledgeLimit")}</dt><dd>{t("knowledgeActualInputUnknown")}</dd></div></dl><button className="knowledge-read-process" type="button" onClick={() => setView("evidence")}>{t("knowledgeReadProcess")}<ChevronRight size={13} /></button></div>;

  return <div className={`knowledge-prototype ${episode ? "reading" : "browsing"}`} data-knowledge-ready={ready ? "true" : "false"}>
    {episode ? <>
      <header className="knowledge-heading"><button className="knowledge-back" type="button" onClick={back}><ArrowLeft size={14} />{t(query ? "knowledgeBackQuery" : "knowledgeAllExperiences")}</button>{badge}</header>
      <div className="knowledge-story"><span>{episode.agent}</span><h2>{episode.title}</h2></div>
      <div className="knowledge-detail-tabs" role="tablist" aria-label={t("knowledgeDetailViews")} onKeyDown={event => { const index = views.indexOf(view); const next = event.key === "Home" ? views[0] : event.key === "End" ? views[2] : event.key === "ArrowRight" ? views[(index + 1) % views.length] : event.key === "ArrowLeft" ? views[(index + views.length - 1) % views.length] : null; if (!next) return; event.preventDefault(); setView(next); document.getElementById(`knowledge-tab-${next}`)?.focus(); }}>
        {views.map(item => <button id={`knowledge-tab-${item}`} type="button" key={item} role="tab" aria-selected={view === item} aria-controls="knowledge-detail-panel" tabIndex={view === item ? 0 : -1} onClick={() => setView(item)}>{t(viewKeys[item])}</button>)}
      </div>
      <section id="knowledge-detail-panel" role="tabpanel" aria-labelledby={`knowledge-tab-${view}`}>
        {view === "evidence" ? <div className="knowledge-evidence"><div className="knowledge-section-heading"><span>{t("knowledgeRealSource")}</span><button className="knowledge-text-button" type="button" aria-expanded={refs} onClick={() => setRefs(!refs)}>{t(refs ? "knowledgeHideRefs" : "knowledgeShowRefs")}</button></div><ol>{slice?.events.map(event => <li key={event.id} className="recorded" data-event-id={event.id}><div className="knowledge-step-heading"><strong>{event.tool?.name || evidenceLabel(event.kind, t)}</strong>{event.id === slice.focus_id ? <span className="knowledge-step-match">{t("knowledgeMatchedStep")}</span> : null}</div><pre><HighlightedText text={event.id === slice.focus_id && event.id === episode.session?.matched_ids[0] && event.omissions?.includes("arguments_omitted") && episode.session?.matched_preview ? episode.session.matched_preview : event.text} words={queryWords} />{event.tool?.arguments ? <HighlightedText text={`${event.text ? "\n" : ""}${typeof event.tool.arguments === "string" ? event.tool.arguments : JSON.stringify(event.tool.arguments, null, 2)}`} words={queryWords} /> : null}</pre>{refs ? <small>{evidenceLabel(event.protocol_role || event.role, t)} · {event.actor.kind === "unknown" ? t("knowledgeActorUnknown") : evidenceLabel(event.actor.kind, t)}{"\n"}{event.source.id} · L{event.source.line} · {event.source.offset} · {event.id}{event.pair_id ? `\n${t("knowledgePair")}: ${event.pair_id}` : ""}</small> : null}{event.omissions?.length ? <small className="knowledge-gap">{event.omissions.join(" · ")}</small> : null}<button type="button" className="knowledge-text-button knowledge-raw-button" disabled={busy.raw} onClick={() => void readRaw(event.id)}>{t("knowledgeRaw")}</button></li>)}</ol><div className="knowledge-slice-actions"><button className="knowledge-text-button" type="button" disabled={!slice?.before || busy.read} onClick={() => slice?.before && void readEvidence(slice.before)}>{t("knowledgeEarlier")}</button><button className="knowledge-text-button" type="button" disabled={!slice?.after || busy.read} onClick={() => slice?.after && void readEvidence(slice.after)}>{t("knowledgeLater")}</button></div>{slice?.truncated ? <p className="knowledge-gap">{t("knowledgeTruncated")}</p> : null}<CoverageNotice coverage={slice?.coverage} t={t} />
          {raw?.slice.raw_chunk ? <div className="knowledge-raw-window" data-raw-event-id={raw.slice.focus_id}><div className="knowledge-section-heading"><span>{t("knowledgeRaw")}</span><span>{raw.slice.raw_chunk.offset} / {raw.slice.raw_chunk.total_bytes} {t("knowledgeBytes")}</span></div><pre className="knowledge-raw">{raw.text}</pre>{raw.clipped ? <p className="knowledge-gap">{t("knowledgeRawWindow")}</p> : null}{raw.slice.raw_chunk.next_offset !== undefined ? <button type="button" className="knowledge-text-button" disabled={busy.raw} onClick={() => void readRaw(raw.slice.focus_id, raw.slice.raw_chunk!.next_offset!)}>{t("knowledgeNextBytes")} · {raw.slice.raw_chunk.next_offset}</button> : null}<CoverageNotice coverage={raw.slice.coverage} t={t} /></div> : null}
        </div> : <>
          {view === "relations" ? graph ? <Suspense fallback={<div className="knowledge-graph-loading" role="status">{t("knowledgeGraphLoading")}</div>}><KnowledgeGraph graph={graph} knowledge={notes} selectedId={selectedID} onPick={id => void pick(id)} onRead={readSource} onReady={setGraphMounted} t={t} /></Suspense> : error ? null : <p className="knowledge-gap" role="status">{t("knowledgeGraphLoading")}</p> : null}
          {inspector}
          {view === "insight" ? <>
            <details className="knowledge-disclosure" open={Boolean(record)}><summary>{t("knowledgeLocalRecords")}</summary><div className="knowledge-records">{notes.map(note => <button className="knowledge-record" type="button" key={note.id} data-knowledge-id={note.id} aria-pressed={selectedID === note.id} onClick={() => void pick(note.id)}><span>{evidenceLabel(note.kind, t)} · {evidenceLabel(note.state, t)} · {evidenceLabel(note.evidence_state, t)}</span><strong>{note.text}</strong></button>)}</div>{!notes.length && !busy.details ? <p className="knowledge-gap">{t("knowledgeNoLocalRecords")}</p> : null}{notesNext ? <button className="knowledge-text-button" type="button" disabled={busy.notes} onClick={() => void moreNotes()}>{t("knowledgeMoreRecords")}</button> : null}<CoverageNotice coverage={notesCoverage} t={t} /></details>
            <ContextDetails contexts={contexts} scope={contextScope} manifest={manifest} pending={Boolean(busy.contexts || busy.manifest)} onScope={changeContextScope} onOpen={id => void openContext(id)} onMore={offset => manifest && void openContext(manifest.focus_id, offset)} onRead={readSource} t={t} />{contextNext ? <button type="button" className="knowledge-text-button" disabled={busy.contexts} onClick={() => void moreContexts()}>{t("knowledgeMoreContexts")}</button> : null}<CoverageNotice coverage={contextCoverage} t={t} />
            <AttentionDetails result={attention} onRead={readSource} t={t} />
          </> : null}
        </>}
      </section>
    </> : <>
      <header className="knowledge-heading"><h2>{t("knowledgeSimpleTitle")}</h2>{badge}</header>
      {!access ? <div className="knowledge-empty" role="status"><strong>{t("knowledgeCheckingAccess")}</strong></div> : !access.enabled || !access.authorized ? <div className="knowledge-empty">
        <strong>{t(access.authorized ? "knowledgeEnableTitle" : "knowledgeOpenFromAppTitle")}</strong>
        <p>{t(access.authorized ? "knowledgeEnableDetail" : "knowledgeOpenFromApp")}</p>
        {access.authorized ? <button className="knowledge-read-process" type="button" disabled={pending} onClick={() => void setEnabled(true)}>{t("knowledgeEnable")}</button> : null}
      </div> : <>
        <div className="knowledge-search"><Search size={17} /><input ref={search} type="search" value={query} aria-label={t("knowledgeSimpleSearch")} placeholder={t("knowledgeSimpleSearch")} onChange={event => changeQuery(event.target.value)} onKeyDown={event => { if (event.key === "Escape") changeQuery(""); if (event.key === "Enter" && !busy.access && episodes[0]) open(episodes[0]); }} />{query ? <button type="button" aria-label={t("knowledgeClear")} onClick={() => { changeQuery(""); search.current?.focus(); }}><X size={14} /></button> : <kbd>↵</kbd>}</div>
        <p className="knowledge-search-tip">{t("knowledgePlainSearchHelp")}</p>
        <details className="knowledge-advanced-search"><summary>{t("knowledgeQueryHelp")}</summary><div className="knowledge-query-help"><p>{t("knowledgeLiveQuerySyntax")}</p><div>{["tool:exec_command", "skill:proxy-debugger predicate:mention", "in:knowledge kind:candidate", "in:knowledge state:withdrawn", "in:contexts context-scope:actual_input"].map(value => <button type="button" key={value} onClick={() => changeQuery(value)}><code>{value}</code></button>)}</div></div></details>
        {indexPending ? <p className="knowledge-index-progress" role="status">{result?.coverage.gaps.includes("index_storage_pending") ? t("knowledgeStoragePending") : result?.coverage.index ? formatCopy(t(searchIndexPending ? "knowledgeIndexProgress" : "knowledgeDecodeProgress"), { ready: searchIndexPending ? result.coverage.index.searchable_sources : result.coverage.index.decoded_sources, known: result.coverage.index.known_sources }) : t("knowledgeIndexPending")}</p> : searching && !episodes.length ? <div className="knowledge-empty" role="status"><strong>{t("knowledgeSearching")}</strong></div> : null}
        {episodes.length ? <div className="knowledge-section-heading knowledge-list-heading"><span>{t(query ? "knowledgeRelatedExperiences" : "knowledgeBrowseExperiences")}</span><span role="status">{result?.matched_total !== undefined && querySelectorSafeCollection(query) === "sessions" ? formatCopy(t(indexPending ? "knowledgePreparedCount" : "knowledgePageCount"), { shown: episodes.length, total: result.matched_total }) : formatCopy(t("knowledgeLiveCount"), { count: episodes.length })}</span></div> : null}
        <div className="knowledge-experiences">{episodes.map(item => <button type="button" className="knowledge-experience" key={item.id} data-session-id={item.id} data-match-event-id={item.eventIDs[0]} onClick={() => open(item)}>
          <span className="knowledge-result-body"><strong className="knowledge-match-preview" data-matched-preview><HighlightedText text={item.preview || t("knowledgeOpenMatch")} words={queryWords} /></strong><ChevronRight size={15} /></span>
          <span className="knowledge-result-session">{item.agent}{item.agent ? " · " : ""}{item.title === item.id ? t("knowledgeKindSession") : item.title}</span>
          <span className="knowledge-experience-meta"><span>{item.knowledge[0] ? `${evidenceLabel(item.knowledge[0].kind, t)} · ${evidenceLabel(item.knowledge[0].state, t)}` : ""}</span><span>{formatCopy(t(querySelectorSafeCollection(query) === "sessions" && item.session?.matched_count != null ? "knowledgeMatchingCount" : "knowledgeReferenceCount"), { count: querySelectorSafeCollection(query) === "sessions" && item.session?.matched_count != null ? item.session.matched_count : item.eventIDs.length })}</span></span>
        </button>)}</div>
        {result && !episodes.length && !busy.query && !indexPending ? <div className="knowledge-empty"><strong>{t("knowledgeNoMatches")}</strong><p>{t("knowledgeEmptyLive")}</p></div> : null}
        {result?.next ? <button className="knowledge-text-button" type="button" disabled={busy.query} onClick={() => void nextPage()}>{t("knowledgeLater")}</button> : null}
      </>}
    </>}
    {pending && (episode || Boolean(access?.enabled && access.authorized && !searching)) ? <p className="knowledge-gap" role="status">{t("knowledgeLoading")}</p> : null}
    {error ? <p className="knowledge-gap" role="alert">{t(errorKeys[error] ?? "knowledgeReadError")}<button type="button" className="knowledge-text-button" onClick={() => { back(); setRevision(value => value + 1); }}>{t("refresh")}</button></p> : null}
    <CoverageNotice coverage={episode?.session?.coverage || result?.coverage} t={t} />
    {access?.enabled && access.authorized && !episode ? <div className="knowledge-local-actions"><button type="button" className="knowledge-text-button" onClick={() => setRevision(value => value + 1)} disabled={pending}>{t("refresh")}</button><button type="button" className="knowledge-text-button" onClick={() => void setEnabled(false)} disabled={pending}>{t("knowledgeDisable")}</button></div> : null}
  </div>;
}

function querySelectorSafeCollection(query: string): string | undefined {
  try { return querySelector(query).collection; } catch { return undefined; }
}
