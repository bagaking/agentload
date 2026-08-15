import { useEffect, useMemo, useState } from "react";
import { Background, BackgroundVariant, Handle, MarkerType, Position, ReactFlow, type Edge, type Node, type NodeProps, type ReactFlowInstance } from "@xyflow/react";
import { Check, Crosshair, FileText, FlaskConical, ZoomIn, ZoomOut } from "lucide-react";
import { formatCopy } from "../lib/format";
import { SourceButton, CoverageNotice } from "./EvidenceViews";
import { evidenceLabel } from "./knowledgeLabels";
import type { Knowledge, RelationResult, SourceRef } from "./trajectoryApi";
import "@xyflow/react/dist/style.css";

type GraphItem = { id: string; title: string; kind: string; status: string; source?: SourceRef };
type GraphData = { item: GraphItem; picked: boolean; pick: () => void; label: string; state: string };
type GraphNode = Node<GraphData, "evidence">;

function EvidenceNode({ data }: NodeProps<GraphNode>) {
  const Icon = data.item.kind === "candidate" ? FlaskConical : FileText;
  return <div className={`knowledge-node ${data.item.kind} ${data.picked ? "picked" : ""}`} data-graph-node-id={data.item.id}>
    <Handle type="target" position={Position.Top} isConnectable={false} />
    <button type="button" className="nodrag nopan" onClick={data.pick} aria-pressed={data.picked} aria-label={`${data.label}: ${data.item.title}`}><span className="knowledge-node-kind"><Icon size={11} />{data.label}{data.picked ? <Check size={11} /> : null}</span><strong>{data.item.title}</strong><small>{data.state}</small></button>
    <Handle type="source" position={Position.Bottom} isConnectable={false} />
  </div>;
}
const nodeTypes = { evidence: EvidenceNode };

export function KnowledgeGraph({ graph, knowledge, selectedId, onPick, onRead, onReady, t }: { graph: RelationResult; knowledge: Knowledge[]; selectedId: string; onPick: (id: string) => void; onRead: (id: string) => void; onReady: (ready: boolean) => void; t: (key: string) => string }) {
  const [flow, setFlow] = useState<ReactFlowInstance<GraphNode, Edge> | null>(null);
  useEffect(() => { onReady(true); return () => onReady(false); }, [onReady]);
  const projection = useMemo(() => {
    const items = new Map<string, GraphItem>();
    for (const node of graph.nodes) items.set(node.id, { id: node.id, kind: node.kind, title: `L${node.source.line} · ${evidenceLabel(node.kind, t)}`, status: node.actor.kind === "unknown" ? t("knowledgeActorUnknown") : evidenceLabel(node.actor.kind, t), source: node.source });
    for (const record of knowledge) {
      items.set(record.id, { id: record.id, title: record.text, kind: record.kind, status: evidenceLabel(record.state, t) });
      for (const source of record.sources) if (!items.has(source.event_id)) items.set(source.event_id, { id: source.event_id, kind: "source", title: `${t("knowledgeSource")} · L${source.source.line}`, status: evidenceLabel(source.status, t), source: source.source });
    }
    const ordered = [...items.values()].sort((a, b) => a.id.localeCompare(b.id));
    const focus = graph.focus_id;
    const bounded = ordered.slice(0, 48);
    const focusItem = focus ? items.get(focus) : undefined;
    if (focusItem && !bounded.some(item => item.id === focus)) bounded[bounded.length - 1] = focusItem;
    return { items: bounded, omitted: Math.max(0, ordered.length - bounded.length) };
  }, [graph, knowledge, t]);
  const ids = new Set(projection.items.map(item => item.id));
  const nodes: GraphNode[] = projection.items.map((item, index) => ({ id: item.id, type: "evidence", position: { x: (index % 3) * 165, y: Math.floor(index / 3) * 105 }, selected: item.id === selectedId, style: { width: 145 }, data: { item, picked: item.id === selectedId, pick: () => onPick(item.id), label: evidenceLabel(item.kind, t), state: item.status } }));
  const edges: Edge[] = [];
  for (const relation of graph.relations) if (relation.status === "resolved") {
    for (const target of relation.target_ids) if (ids.has(relation.from) && ids.has(target)) edges.push({ id: `${relation.id}:${target}`, source: relation.from, target, label: `${evidenceLabel(relation.kind, t)} · ${evidenceLabel(relation.status, t)}`, type: "smoothstep", markerEnd: { type: MarkerType.ArrowClosed, color: "var(--knowledge-edge)" }, style: { stroke: "var(--knowledge-edge)", strokeWidth: 1.3 } });
  }
  for (const record of knowledge) {
    for (const source of record.sources) if (ids.has(record.id) && ids.has(source.event_id)) edges.push({ id: `${record.id}:source:${source.event_id}`, source: record.id, target: source.event_id, label: `${t("knowledgeCitesSource")} · ${evidenceLabel(source.status, t)}`, type: "smoothstep", style: { stroke: "var(--run)", strokeDasharray: "4 4", strokeWidth: 1.3 } });
    for (const link of record.links ?? []) if (ids.has(record.id) && ids.has(link.target_id)) edges.push({ id: `${record.id}:${link.kind}:${link.target_id}`, source: record.id, target: link.target_id, label: `${evidenceLabel(link.kind, t)} · ${evidenceLabel(link.status, t)}`, type: "smoothstep", style: { stroke: "var(--run)", strokeDasharray: "4 4", strokeWidth: 1.3 } });
  }
  for (const edge of edges) Object.assign(edge, { labelStyle: { fill: "var(--fg-muted)", fontSize: 9 }, labelBgStyle: { fill: "var(--surface)" }, labelBgPadding: [4, 2], labelBgBorderRadius: 3, focusable: false });
  return <div className="knowledge-real-graph">
    <p className="knowledge-gap">{t("knowledgeGraphScope")}</p>
    {nodes.length ? <div className="knowledge-graph" role="region" aria-label={t("knowledgeGraph")}><ReactFlow<GraphNode, Edge> nodes={nodes} edges={edges} nodeTypes={nodeTypes} onInit={setFlow} fitView fitViewOptions={{ padding: 0.12, maxZoom: 1 }} minZoom={0.2} maxZoom={1.8} nodesDraggable={false} nodesConnectable={false} nodesFocusable={false} edgesFocusable={false} panOnDrag zoomOnScroll={false} zoomOnPinch zoomOnDoubleClick={false} elementsSelectable={false} deleteKeyCode={null} preventScrolling={false}><Background variant={BackgroundVariant.Dots} gap={17} size={0.7} color="var(--console-line)" /></ReactFlow><div className="knowledge-graph-controls"><button type="button" onClick={() => void flow?.zoomIn({ duration: 0 })} aria-label={t("knowledgeZoomIn")}><ZoomIn size={13} /></button><button type="button" onClick={() => void flow?.zoomOut({ duration: 0 })} aria-label={t("knowledgeZoomOut")}><ZoomOut size={13} /></button><button type="button" onClick={() => void flow?.fitView({ padding: 0.12, duration: 0 })} aria-label={t("knowledgeFit")}><Crosshair size={13} /></button></div></div> : <p className="knowledge-gap">{t("knowledgeNoRelations")}</p>}
    <div className="knowledge-legend"><span><i />{t("knowledgeNativeRelation")}</span><span><i className="candidate" />{t("knowledgeLocalReference")}</span></div>
    {projection.omitted ? <p className="knowledge-gap">{formatCopy(t("knowledgeOmittedCount"), { count: projection.omitted })}</p> : null}
    <div className="knowledge-relation-list">{graph.relations.map(relation => <div key={relation.id} data-relation-id={relation.id} data-evidence-status={relation.status}><strong>{evidenceLabel(relation.kind, t)} · {evidenceLabel(relation.status, t)}</strong><small>{relation.native_field}{relation.outside_window ? ` · ${t("knowledgeOutsideWindow")}` : ""}</small><SourceButton id={relation.from} source={relation.source} onRead={onRead} t={t} />{relation.target_ids.map(id => <button className="knowledge-record-link" type="button" key={id} onClick={() => onPick(id)}>{t("knowledgeTarget")}<code>{id}</code></button>)}{relation.status !== "resolved" ? <p>{relation.target.kind}: <code>{relation.target.id}</code>{relation.candidate_ids?.length ? ` · ${formatCopy(t("knowledgePossibleTargets"), { count: relation.candidate_ids.length })}` : ""}</p> : null}</div>)}</div>
    {!graph.relations.length ? <p className="knowledge-gap">{t("knowledgeNoNativeRelations")}</p> : null}
    <CoverageNotice coverage={graph.coverage} t={t} />
  </div>;
}
