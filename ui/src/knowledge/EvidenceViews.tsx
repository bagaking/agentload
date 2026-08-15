import { ChevronRight } from "lucide-react";
import { formatCopy } from "../lib/format";
import { evidenceLabel } from "./knowledgeLabels";
import type { AttentionResult, ContextManifest, Coverage, Entity, Knowledge, SourceRef, TrajectoryContext } from "./trajectoryApi";

type Translator = (key: string) => string;

export function CoverageNotice({ coverage, t }: { coverage?: Coverage; t: Translator }) {
  if (!coverage || (coverage.complete && !coverage.omitted)) return null;
  return <details className="knowledge-coverage"><summary>{t("knowledgeCoverageGap")}{coverage.omitted ? ` · ${formatCopy(t("knowledgeOmittedCount"), { count: coverage.omitted })}` : ""}</summary><p>{coverage.scope}</p><ul>{coverage.gaps.map((gap, i) => <li key={`${gap}-${i}`}>{gap}</li>)}</ul></details>;
}

export function SourceButton({ id, source, status, onRead, t }: { id: string; source?: SourceRef; status?: string; onRead: (id: string) => void; t: Translator }) {
  return <button type="button" className="knowledge-source-link" data-source-event-id={id} onClick={() => onRead(id)}><span>{t("knowledgeSource")}{source ? ` · L${source.line}` : ""}{status ? ` · ${evidenceLabel(status, t)}` : ""}</span><code>{id}</code><ChevronRight size={12} /></button>;
}

export function KnowledgeDetails({ record, onRead, onPick, t }: { record: Knowledge; onRead: (id: string) => void; onPick: (id: string) => void; t: Translator }) {
  const scope = record.applicability;
  const limitKey = record.kind === "verification" ? "knowledgeScopedVerification" : record.kind === "counterexample" ? "knowledgeCounterexampleLimit" : record.kind === "observation" ? "knowledgeObservationLimit" : "knowledgeCandidateLimit";
  return <div className={`knowledge-inspector ${record.kind}`} data-knowledge-id={record.id}>
    <div className="knowledge-section-heading"><span>{evidenceLabel(record.kind, t)} · {evidenceLabel(record.state, t)}</span><span>{evidenceLabel(record.origin, t)}</span></div>
    <h3>{record.text}</h3>
    <dl><div><dt>{t("knowledgeEvidenceState")}</dt><dd>{evidenceLabel(record.evidence_state, t)}</dd></div><div><dt>{t("knowledgeScope")}</dt><dd>{record.experience ? <>{record.experience.scope.kind} · <code>{record.experience.scope.id}</code></> : scope ? <>{scope.version ? <p>{t("knowledgeVersion")}: {scope.version}</p> : null}{Object.entries(scope.configuration ?? {}).map(([key, value]) => <p key={`c-${key}`}>{key}: {value}</p>)}{Object.entries(scope.environment ?? {}).map(([key, value]) => <p key={`e-${key}`}>{key}: {value}</p>)}{scope.evaluation_refs?.map(value => <p key={value}>{value}</p>)}{scope.artifact_refs?.map(value => <p key={value}>{value}</p>)}</> : t("knowledgeScopeUnprovided")}</dd></div></dl>
    <p className="knowledge-gap">{t(limitKey)}</p>
    {record.experience ? <div className="knowledge-experience-basis"><p>{t("knowledgeRecordedPattern")} · {record.experience.pattern}</p><p>{t("knowledgeScope")}: {record.experience.scope.kind} · <code>{record.experience.scope.id}</code></p><p>{t("knowledgeCausality")}: {evidenceLabel(record.experience.causality, t)} · {t("knowledgeTaskCompletion")}: {evidenceLabel(record.experience.task_completion, t)}</p>{record.experience.steps.map(step => <div key={step.call_event_id}><span>{step.tool} · {step.outcome} · <code>{step.outcome_field}</code></span><SourceButton id={step.result_event_id} onRead={onRead} t={t} /></div>)}</div> : null}
    {record.rule_id ? <small className="knowledge-rule">{record.rule_id} · {record.rule_version}</small> : null}
    <div className="knowledge-source-list">{record.sources.map(source => <SourceButton key={source.event_id} id={source.event_id} source={source.source} status={source.status} onRead={onRead} t={t} />)}</div>
    {record.links?.map(link => <button className="knowledge-record-link" key={`${link.kind}-${link.target_id}`} type="button" onClick={() => onPick(link.target_id)}>{evidenceLabel(link.kind, t)} · {evidenceLabel(link.status, t)}<code>{link.target_id}</code></button>)}
    {record.omissions?.length ? <p className="knowledge-gap">{record.omissions.join(" · ")}</p> : null}
  </div>;
}

export function EntityDetails({ entity, onRead, t }: { entity: Entity; onRead: (id: string) => void; t: Translator }) {
  return <div className="knowledge-inspector" data-entity-id={entity.id}><div className="knowledge-section-heading">{evidenceLabel(entity.kind, t)}</div><h3>{entity.label}</h3><p>{entity.literal}</p><dl><div><dt>{t("knowledgeScope")}</dt><dd>{entity.scope}</dd></div></dl>{entity.occurrences.map(occurrence => <div className="knowledge-occurrence" key={occurrence.id}><span>{evidenceLabel(occurrence.predicate, t)}</span><SourceButton id={occurrence.event_id} source={occurrence.source} onRead={onRead} t={t} /></div>)}<p className="knowledge-gap">{t("knowledgeMentionLimit")}</p><CoverageNotice coverage={entity.coverage} t={t} /></div>;
}

export function ContextDetails({ contexts, scope, manifest, pending, onScope, onOpen, onMore, onRead, t }: { contexts: TrajectoryContext[]; scope: string; manifest: ContextManifest | null; pending: boolean; onScope: (scope: string) => void; onOpen: (id: string) => void; onMore: (offset: number) => void; onRead: (id: string) => void; t: Translator }) {
  const c = manifest?.context;
  return <details className="knowledge-disclosure"><summary>{t("knowledgeContext")}</summary><p className="knowledge-gap">{t("knowledgeActualInputUnknown")}</p><label className="knowledge-scope-control">{t("knowledgeContextScope")}<select value={scope} aria-label={t("knowledgeContextScope")} onChange={event => onScope(event.target.value)}>{["actual_input", "archive", "workspace", "query_window"].map(item => <option key={item} value={item}>{evidenceLabel(item, t)}</option>)}</select></label>
    <div className="knowledge-context-list">{contexts.map(context => <button type="button" key={context.id} data-context-id={context.id} className="knowledge-context" aria-pressed={c?.id === context.id} disabled={pending} onClick={() => onOpen(context.id)}><span>{evidenceLabel(context.scope, t)} · {evidenceLabel(context.membership, t)}</span><small>{context.native_revision || context.source_revision} · L{context.source.line}</small></button>)}</div>
    {!contexts.length && !pending ? <p className="knowledge-gap">{t("knowledgeNoContextEvidence")}</p> : null}
    {c && manifest ? <div className="knowledge-manifest" data-context-id={c.id}><div className="knowledge-section-heading"><span>{evidenceLabel(c.scope, t)} · {evidenceLabel(c.membership, t)}</span><span>{formatCopy(t("knowledgeKnownMembers"), { count: c.known_members })}</span></div><code>{c.id}</code>{c.branch ? <p>{t("knowledgeBranch")}: {c.branch.id}</p> : null}{c.workspace ? <p>{c.workspace.path}</p> : null}{c.transformation ? <p className="knowledge-gap">{evidenceLabel(c.transformation.kind, t)} · {t("knowledgeOriginalMembers")}: {evidenceLabel(c.transformation.originals_status, t)}</p> : null}
      {manifest.members.map(member => <div className="knowledge-context-member" key={member.id}><span>{evidenceLabel(member.visibility, t)} · {evidenceLabel(member.status, t)}</span>{member.event_ids.map(id => <SourceButton key={id} id={id} onRead={onRead} t={t} />)}{!member.event_ids.length ? <small>{member.target.kind}: {member.target.id}</small> : null}{member.native_field ? <small>{member.native_field}</small> : null}</div>)}
      <div className="knowledge-slice-actions"><button type="button" className="knowledge-text-button" disabled={!c.before_id || pending} onClick={() => c.before_id && onOpen(c.before_id)}>{t("knowledgeEarlierRevision")}</button><button type="button" className="knowledge-text-button" disabled={!c.after_id || pending} onClick={() => c.after_id && onOpen(c.after_id)}>{t("knowledgeLaterRevision")}</button>{manifest.next_offset !== undefined ? <button type="button" className="knowledge-text-button" disabled={pending} onClick={() => onMore(manifest.next_offset!)}>{t("knowledgeMoreMembers")}</button> : null}</div><CoverageNotice coverage={manifest.coverage} t={t} />
    </div> : null}
  </details>;
}

export function AttentionDetails({ result, onRead, t }: { result: AttentionResult | null; onRead: (id: string) => void; t: Translator }) {
  const attention = result?.attention;
  if (!attention) return null;
  return <details className="knowledge-disclosure"><summary>{t("knowledgeObservedProgress")}</summary><p>{t("knowledgeLiveness")}: {evidenceLabel(attention.liveness, t)}</p>{attention.progress.turns.map((turn, i) => <p key={`${turn.turn_id}-${i}`}>{turn.turn_id || t("knowledgeUnknown")} · {evidenceLabel(turn.status, t)}</p>)}{attention.interventions.map(item => <div key={item.id} className="knowledge-intervention" data-attention-id={item.id}><strong>{evidenceLabel(item.kind, t)} · {evidenceLabel(item.status, t)}</strong>{item.tool ? <p>{item.tool}</p> : null}{item.evidence.map(ref => <SourceButton key={ref.event_id} id={ref.event_id} source={ref.source} onRead={onRead} t={t} />)}</div>)}{!attention.interventions.length ? <p>{t("knowledgeNoIntervention")}</p> : null}<p className="knowledge-gap">{t("knowledgeAttentionLimit")}</p><small>{result?.rules.version}</small><CoverageNotice coverage={result?.coverage} t={t} /></details>;
}
