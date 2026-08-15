package trajectory

import (
	"agentload/internal/snapshot"
	"encoding/json"
	"strings"
)

func nativeIntervention(e *snapshot.TrajectoryEvent, payload map[string]json.RawMessage, prefix string) {
	if e.Kind != "permission_request" && e.Kind != "permission_response" && e.Kind != "waiting_input" && e.Kind != "input_response" {
		return
	}
	key := "request_id"
	id := nativeString(payload[key])
	if id == "" && strings.HasPrefix(e.Kind, "permission_") {
		key = "approval_id"
		id = nativeString(payload[key])
	}
	if id == "" {
		return
	}
	e.Attention = &snapshot.TrajectoryAttentionEvidence{}
	if e.Kind == "permission_request" || e.Kind == "waiting_input" {
		e.Attention.RequestID, e.Attention.RequestIDField = id, prefix+"/"+key
	} else {
		e.Attention.ResponseToID, e.Attention.ResponseToField = id, prefix+"/"+key
	}
}

func nativeString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}
func withRelation(e *snapshot.TrajectoryEvent, kind string, target snapshot.TrajectoryReference, path string) {
	if target.ID == "" || path == "" {
		return
	}
	if e.Evidence == nil {
		e.Evidence = &snapshot.TrajectoryEvidence{}
	}
	e.Evidence.Relations = append(e.Evidence.Relations, snapshot.TrajectoryRelationEvidence{Kind: kind, Target: target, NativeField: path})
}
func nativeWorkspace(raw json.RawMessage, path string) *snapshot.TrajectoryWorkspace {
	if cwd := nativeString(raw); cwd != "" {
		return &snapshot.TrajectoryWorkspace{Path: cwd, NativeField: path}
	}
	return nil
}

// Each decoder chooses this helper only for its verified session metadata shape.
func rolloutMetadata(e *snapshot.TrajectoryEvent, p map[string]json.RawMessage, agent string) {
	e.Workspace = nativeWorkspace(p["cwd"], "/payload/cwd")
	parent := nativeString(p["parent_thread_id"])
	path := "/payload/parent_thread_id"
	if parent == "" {
		source := object(p["source"])
		subagent := object(source["subagent"])
		spawn := object(subagent["thread_spawn"])
		parent = nativeString(spawn["parent_thread_id"])
		path = "/payload/source/subagent/thread_spawn/parent_thread_id"
	}
	if parent != "" {
		withRelation(e, "parent", snapshot.TrajectoryReference{Kind: "session", ID: parent, SessionNativeID: parent, Agent: agent}, path)
	}
}
