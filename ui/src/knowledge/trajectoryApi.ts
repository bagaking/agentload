export type Coverage = { complete: boolean; scope: string; gaps: string[]; omitted: number; index?: { known_sources: number; decoded_sources: number; searchable_sources: number; decoded_events: number; searchable_events: number } };
export type SourceRef = { id: string; generation: string; line: number; offset: number; length: number; block: number; digest: string; native_type: string };
export type Actor = { id?: string; kind: string };
export type Branch = { id: string; native_field: string };
export type TrajectoryEvent = { id: string; session_id: string; native_id?: string; native_envelope_id?: string; turn_id?: string; kind: string; role: string; protocol_role?: string; actor: Actor; text: string; timestamp?: string; tool?: { name: string; call_id?: string; arguments?: unknown }; pair_id?: string; outcome?: string; source: SourceRef; raw?: string; omissions?: string[] };
export type TrajectorySession = { id: string; native_id?: string; agent: string; title: string; last_action: string; last_event?: string; event_count: number; matched_count: number; matched_ids: string[]; matched_preview?: string; tools: string[]; coverage: Coverage };
export type Selector = { count?: boolean; collection?: string; text?: string; tool?: string; skill?: string; kind?: string; state?: string; agent?: string; session_id?: string; role?: string; actor_id?: string; actor_kind?: string; relation_kind?: string; entity_kind?: string; entity_id?: string; predicate?: string; context_id?: string; context_scope?: string; limit?: number; cursor?: string };
export type EntityOccurrence = { id: string; entity_id: string; event_id: string; session_id: string; kind: string; literal: string; label: string; scope: string; predicate: string; native_field: string; source: SourceRef };
export type Entity = { id: string; kind: string; literal: string; label: string; scope: string; count: number; occurrences: EntityOccurrence[]; occurrence_query: Selector; coverage: Coverage };
export type KnowledgeSource = { event_id: string; session_id: string; agent: string; source: SourceRef; status: string };
export type KnowledgeLink = { kind: string; target_id: string; status: string };
export type KnowledgeStep = { call_event_id: string; result_event_id: string; tool: string; outcome: string; outcome_field: string };
export type Knowledge = { id: string; kind: string; state: string; origin: string; text: string; sources: KnowledgeSource[]; links?: KnowledgeLink[]; evidence_state: string; scope_status: string; rule_id?: string; rule_version?: string; omissions?: string[]; applicability?: { configuration?: Record<string, string>; environment?: Record<string, string>; version?: string; evaluation_refs?: string[]; artifact_refs?: string[] }; experience?: { pattern: string; basis: string; scope: { kind: string; id: string }; steps: KnowledgeStep[]; counterexamples?: KnowledgeStep[]; causality: string; task_completion: string; verification: string } };
export type NativeReference = { kind: string; id: string; source_id?: string; generation?: string; session_native_id?: string; agent?: string };
export type RelationNode = { id: string; type: string; session_id: string; kind: string; role?: string; protocol_role?: string; native_id?: string; native_envelope_id?: string; actor: Actor; source: SourceRef; branch?: Branch };
export type Relation = { id: string; from: string; kind: string; target: NativeReference; target_ids: string[]; candidate_ids?: string[]; status: string; native_field: string; source: SourceRef; outside_window: boolean; omissions?: string[] };
export type RelationResult = { nodes: RelationNode[]; relations: Relation[]; coverage: Coverage; revision: string; next?: string; focus_id?: string; truncated: boolean; byte_limit: number };
export type TrajectoryContext = { id: string; scope: string; session_id: string; anchor_id?: string; native_id?: string; native_revision?: string; source_revision: string; membership: string; known_members: number; source: SourceRef; branch?: Branch; workspace?: { path: string; native_field: string }; transformation?: { kind: string; event_id: string; source: SourceRef; before_id: string; after_id: string; originals_status: string }; before_id?: string; after_id?: string; coverage: Coverage };
export type ContextMember = { id: string; event_ids: string[]; target: NativeReference; status: string; visibility: string; native_field?: string; source: SourceRef; candidates?: string[] };
export type ContextManifest = { context?: TrajectoryContext; members: ContextMember[]; coverage: Coverage; revision: string; focus_id: string; before?: string; after?: string; truncated: boolean; byte_limit: number; member_offset: number; next_offset?: number };
export type AttentionReference = { event_id: string; native_field?: string; source: SourceRef };
export type Attention = { id: string; session_id: string; agent: string; progress: { status: string; turns: { turn_id?: string; status: string; starts: AttentionReference[]; ends: AttentionReference[] }[] }; interventions: { id: string; kind: string; status: string; evidence: AttentionReference[]; omissions?: string[]; count?: number; tool?: string }[]; latest_action?: AttentionReference; liveness: string; coverage: Coverage };
export type AttentionResult = { attention?: Attention; coverage: Coverage; rules: { version: string; recovery_conditions: string[] }; revision: string; truncated: boolean };
export type QueryResult = { matched_total?: number; sessions?: TrajectorySession[]; events?: TrajectoryEvent[]; entities?: Entity[]; knowledge?: Knowledge[]; contexts?: TrajectoryContext[]; attention?: Attention[]; nodes?: RelationNode[]; relations?: Relation[]; actors?: { id: string; event_id: string; session_id: string; actor: Actor; role: string; protocol_role?: string; source: SourceRef }[]; coverage: Coverage; revision: string; next?: string };
export type Slice = { events: TrajectoryEvent[]; knowledge?: Knowledge; entity?: Entity; coverage: Coverage; focus_id: string; truncated: boolean; before?: string; after?: string; byte_limit: number; raw_chunk?: { encoding: string; data: string; offset: number; total_bytes: number; next_offset?: number } };
export type Access = { enabled: boolean; authorized: boolean; preference_gap: boolean };

let capability = "";
const storageKey = "agentload.trajectory.capability";
try {
  const params = new URLSearchParams(location.hash.slice(1));
  capability = params.get("trajectory") ?? "";
  if (params.has("trajectory")) {
    params.delete("trajectory");
    history.replaceState(history.state, "", location.pathname + location.search + (params.size ? `#${params}` : ""));
    sessionStorage.setItem(storageKey, capability);
  } else {
    capability = sessionStorage.getItem(storageKey) ?? "";
  }
} catch { /* Memory-only capability works when native webview storage is unavailable. */ }

const headers = () => ({ "Content-Type": "application/json", "X-AgentLoad-Local": "1", ...(capability ? { Authorization: `Bearer ${capability}` } : {}) });
export async function accessPreference(enabled?: boolean, signal?: AbortSignal): Promise<Access> {
  const response = await fetch("/api/trajectory/access", { method: enabled === undefined ? "GET" : "POST", headers: headers(), body: enabled === undefined ? undefined : JSON.stringify({ enabled }), signal });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return response.json() as Promise<Access>;
}
export class TrajectoryError extends Error {
  constructor(message: string, readonly code: number) { super(message); }
}
export async function trajectoryRPC<T>(method: string, params: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch("/api/rpc", { method: "POST", headers: headers(), body: JSON.stringify({ jsonrpc: "2.0", id: "popover", method, params }), signal });
  const body = await response.json() as { result?: T; error?: { code: number; message: string } };
  if (!response.ok || body.error) throw new TrajectoryError(body.error?.message ?? `HTTP ${response.status}`, body.error?.code ?? response.status);
  return body.result as T;
}

// This compiles UI syntax into the Go service's selectors; it never evaluates
// evidence or creates an independent search implementation.
export function querySelector(input: string): Selector {
  const selector: Selector = { collection: "sessions", limit: 20 };
  const words: string[] = [];
  const fields: Record<string, keyof Selector> = { in: "collection", tool: "tool", skill: "skill", kind: "kind", state: "state", agent: "agent", session: "session_id", role: "role", actor: "actor_id", "actor-kind": "actor_kind", "relation-kind": "relation_kind", "entity-kind": "entity_kind", entity: "entity_id", predicate: "predicate", context: "context_id", "context-scope": "context_scope" };
  const seen = new Set<string>();
  for (const token of input.trim().split(/\s+/).filter(Boolean)) {
    const colon = token.indexOf(":");
    if (colon < 0) { words.push(token); continue; }
    const field = fields[token.slice(0, colon).toLowerCase()];
    const value = token.slice(colon + 1);
    if (!field || !value || seen.has(field)) throw new Error("invalid_query");
    seen.add(field);
    Object.assign(selector, { [field]: value });
  }
  selector.text = words.join(" ");
  if (!["sessions", "events", "entities", "knowledge", "contexts", "attention", "relations", "actors"].includes(selector.collection ?? "")) throw new Error("invalid_query");
  return selector;
}
