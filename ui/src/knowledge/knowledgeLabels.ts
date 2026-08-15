const labels: Record<string, string> = {
  observation: "knowledgeObservation", candidate: "knowledgeCandidate", verification: "knowledgeVerificationRecord", counterexample: "knowledgeCounterexample",
  active: "knowledgeActive", withdrawn: "knowledgeWithdrawn", valid: "knowledgeValid", stale: "knowledgeStale", missing: "knowledgeMissing", unavailable: "knowledgeUnavailable",
  unknown: "knowledgeUnknown", partial: "knowledgePartial", complete: "knowledgeComplete", recorded: "knowledgeRecorded", resolved: "knowledgeResolved", ambiguous: "knowledgeAmbiguous", coverage_incomplete: "knowledgePartial", missing_evidence: "knowledgeMissingEvidence", unsupported: "knowledgeUnsupported",
  archive: "knowledgeArchive", actual_input: "knowledgeActualInput", workspace: "knowledgeWorkspace", query_window: "knowledgeQueryWindow",
  archive_record: "knowledgeArchive", input_included: "knowledgeInputIncluded", workspace_reference: "knowledgeWorkspace", retrieval_window: "knowledgeQueryWindow",
  summary: "knowledgeSummary", compaction: "knowledgeCompaction", text: "knowledgeText", reasoning: "knowledgeReasoning", tool_call: "knowledgeToolCall", tool_result: "knowledgeToolResult", session: "knowledgeKindSession", context: "knowledgeContext",
  user: "knowledgeRoleUser", assistant: "knowledgeRoleAssistant", tool: "knowledgeKindTool", system: "knowledgeRoleSystem", human: "knowledgeActorHuman", agent: "knowledgeActorAgent",
  mention: "knowledgeMention", called: "knowledgeCalled", requested_read: "knowledgeRequestedRead", read: "knowledgeRead", requested_load: "knowledgeRequestedLoad", loaded: "knowledgeLoaded",
  skill: "knowledgeKindSkill", path: "knowledgePath", term: "knowledgeTerm", version: "knowledgeVersion", source: "knowledgeSource",
  waiting_permission: "knowledgeWaitingPermission", waiting_input: "knowledgeWaitingInput", stuck_candidate: "knowledgeStuckCandidate", question_hint: "knowledgeQuestionHint", open_observed: "knowledgeOpenObserved", closed_observed: "knowledgeClosedObserved", aborted_observed: "knowledgeAbortedObserved", observed: "knowledgeObserved", hint: "knowledgeHint",
  parent: "knowledgeParent", reply: "knowledgeReply", delegation: "knowledgeDelegation", handoff: "knowledgeHandoff", sent: "knowledgeSent", delivered: "knowledgeDelivered", input_inclusion: "knowledgeInputIncluded", branch_parent: "knowledgeBranchParent", resume: "knowledgeResume",
  verification_of: "knowledgeVerificationOf", counterexample_to: "knowledgeCounterexampleTo", annotation: "knowledgeAuthored", deterministic: "knowledgeDerived", unproven: "knowledgeUnproven", unprovided: "knowledgeUnprovided",
};

export function evidenceLabel(value: string, t: (key: string) => string): string {
  return labels[value] ? t(labels[value]) : value;
}
