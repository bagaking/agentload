package main

import (
	"agentload/internal/historyfile"
	"agentload/internal/snapshot"
	"agentload/internal/trajectory"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *trayApp) trajectorySources(ctx context.Context) trajectory.SourceSet {
	set := trajectory.SourceSet{Coverage: snapshot.TrajectoryCoverage{Complete: true, Scope: "configured local transcript archive", Gaps: []string{}}}
	if !a.trajectoryAccess.isEnabled() || a.observer == nil || a.observer.evidenceIndex == nil {
		set.Coverage.Complete = false
		set.Coverage.Gaps = append(set.Coverage.Gaps, "content_access_disabled")
		return set
	}
	return a.archiveSources(ctx)
}

// Inventory and authorization are shared by archive replay and online hints.
// Each projection still uses its own adapter capability and privacy boundary.
func (a *trayApp) archiveSources(ctx context.Context) trajectory.SourceSet {
	set := trajectory.SourceSet{Coverage: snapshot.TrajectoryCoverage{Complete: true, Scope: "configured local transcript archive", Gaps: []string{}}}
	a.observer.evidenceIndex.requestArchiveCoverage()
	idx := a.observer.evidenceIndex.snapshot(ctx, time.Time{}, nil)
	set.Revision = idx.Revision
	set.CatalogComplete = idx.Complete
	set.Coverage.Complete = idx.Complete
	set.Coverage.Gaps = append(set.Coverage.Gaps, idx.Errors...)
	r := a.observer.adapters
	r.mu.RLock()
	defer r.mu.RUnlock()
	allowed := defaultCodingAgentRegistry(a.cfg).roots()
	physicalDirs := map[string]string{}
	for agent, roots := range allowed {
		for i := range roots {
			roots[i] = canonicalEvidencePath(roots[i])
		}
		allowed[agent] = roots
	}
	reconcileNeeded := false
	for _, entry := range idx.Files {
		if ctx.Err() != nil {
			set.CatalogComplete = false
			set.Coverage.Complete = false
			set.Coverage.Gaps = append(set.Coverage.Gaps, "collection_cancelled")
			break
		}
		file := entry.File
		index, ok := r.byID[file.Tool]
		if !ok {
			continue
		}
		adapter := r.adapters[index]
		// Files in one directory share the same physical parent. Resolve it
		// once per inventory, while still checking redirected leaf symlinks.
		// No authorization result survives this inventory call.
		dir := filepath.Dir(file.Path)
		parent, known := physicalDirs[dir]
		if !known {
			parent = canonicalEvidencePath(dir)
			physicalDirs[dir] = parent
		}
		path := filepath.Join(parent, filepath.Base(file.Path))
		info, err := os.Lstat(path)
		if err != nil {
			reconcileNeeded = reconcileNeeded || os.IsNotExist(err)
			set.CatalogComplete = false
			set.Coverage.Complete = false
			set.Coverage.Gaps = append(set.Coverage.Gaps, "source_unreadable:"+file.Tool)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			path = canonicalEvidencePath(path)
			info, err = os.Stat(path)
			if err != nil {
				reconcileNeeded = reconcileNeeded || os.IsNotExist(err)
				set.CatalogComplete = false
				set.Coverage.Complete = false
				set.Coverage.Gaps = append(set.Coverage.Gaps, "source_unreadable:"+file.Tool)
				continue
			}
		}
		eligible := false
		for _, root := range allowed[file.Tool] {
			relative, err := filepath.Rel(root, path)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				eligible = true
				break
			}
		}
		if !eligible {
			set.Coverage.Complete = false
			set.Coverage.Gaps = append(set.Coverage.Gaps, "outside_content_roots:"+file.Tool)
			continue
		}
		native := genericTranscriptSessionID(file.Path)
		if file.Tool == "codex" {
			native = codexTranscriptSessionID(file.Path)
		}
		if file.Tool == "grok" {
			native = filepath.Base(filepath.Dir(file.Path))
		}
		set.Sources = append(set.Sources, trajectory.Source{Agent: file.Tool, Path: path, NativeID: native, Decoder: adapter.Capabilities.Trajectory, Info: info})
	}
	if reconcileNeeded {
		// Only a complete adapter-owned discovery can establish absence.
		// Retain this call's gap and refresh the stale catalog on the next call,
		// including during offline maintenance where no watcher is running.
		a.observer.evidenceIndex.requestReconcile()
	}
	return set
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func rpcFailure(id json.RawMessage, code int, message string) rpcResponse {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{code, message}}
}
func strictParams(raw json.RawMessage, value any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return trajectory.ErrInvalid
	}
	return nil
}

func (a *trayApp) callTrajectory(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case "traj.query":
		var p snapshot.TrajectorySelector
		if err := strictParams(raw, &p); err != nil {
			return nil, trajectory.ErrInvalid
		}
		if p.Count && (p.ContextID != "" || (p.Collection != "" && p.Collection != "sessions" && p.Collection != "events")) {
			return nil, trajectory.ErrInvalid
		}
		if p.Collection == "actors" {
			return a.trajectory.QueryActors(ctx, p)
		}
		if p.Collection == "relations" {
			return a.trajectory.QueryRelations(ctx, p)
		}
		if p.Collection == "contexts" {
			return a.trajectory.QueryContexts(ctx, p)
		}
		if p.Collection == "attention" {
			return a.trajectory.QueryAttention(ctx, p)
		}
		return a.trajectory.Query(ctx, p)
	case "traj.get":
		p := snapshot.TrajectoryGetParams{Around: 3}
		if err := strictParams(raw, &p); err != nil {
			return nil, trajectory.ErrInvalid
		}
		if p.View == "relations" {
			return a.trajectory.GetRelations(ctx, p)
		}
		if p.View == "context" || strings.HasPrefix(p.ID, "ctx.") {
			return a.trajectory.GetContext(ctx, p)
		}
		if p.View == "attention" {
			if _, provided := objectParams(raw)["around"]; !provided {
				p.Around = 0
			}
			return a.trajectory.GetAttention(ctx, p)
		}
		return a.trajectory.Get(ctx, p)
	case "traj.watch":
		var p snapshot.TrajectoryWatchParams
		if err := strictParams(raw, &p); err != nil {
			return nil, trajectory.ErrInvalid
		}
		return a.trajectory.Watch(ctx, p)
	case "traj.annotate":
		var p snapshot.TrajectoryAnnotationParams
		if err := strictParams(raw, &p); err != nil {
			return nil, trajectory.ErrInvalid
		}
		// Serialize authored writes with the permission change. An accepted
		// write commits before revocation; later invocations see disabled access.
		a.trajectoryAccess.mutationMu.Lock()
		defer a.trajectoryAccess.mutationMu.Unlock()
		if !a.trajectoryAccess.isEnabled() {
			return nil, trajectory.ErrAccess
		}
		return a.trajectory.Annotate(ctx, p)
	default:
		return nil, errors.New("method_not_found")
	}
}

func objectParams(raw json.RawMessage) map[string]json.RawMessage {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	return fields
}

func trajectoryCountRequest(raw json.RawMessage) bool {
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	var q snapshot.TrajectorySelector
	return json.Unmarshal(raw, &req) == nil && req.Method == "traj.query" &&
		strictParams(req.Params, &q) == nil && q.Count
}
func (a *trayApp) rpcOne(ctx context.Context, raw json.RawMessage, rejectCount bool) *rpcResponse {
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
		ID      json.RawMessage `json:"id"`
	}
	if json.Unmarshal(raw, &req) != nil || req.JSONRPC != "2.0" || req.Method == "" {
		res := rpcFailure(nil, -32600, "Invalid Request")
		return &res
	}
	if len(req.ID) > 0 {
		var id any
		d := json.NewDecoder(bytes.NewReader(req.ID))
		d.UseNumber()
		if d.Decode(&id) != nil {
			res := rpcFailure(nil, -32600, "Invalid Request")
			return &res
		}
		switch id.(type) {
		case nil, string, json.Number:
		default:
			res := rpcFailure(nil, -32600, "Invalid Request")
			return &res
		}
	}
	if len(req.Params) > 0 && req.Params[0] != '{' && req.Params[0] != '[' {
		res := rpcFailure(req.ID, -32600, "Invalid Request")
		return &res
	}
	if rejectCount && trajectoryCountRequest(raw) {
		if len(req.ID) == 0 {
			return nil
		}
		res := rpcFailure(req.ID, -32602, "Count requires a single request")
		return &res
	}
	result, err := a.callTrajectory(ctx, req.Method, req.Params)
	if len(req.ID) == 0 {
		return nil
	}
	if err != nil {
		code := -32603
		message := "Internal error"
		switch {
		case errors.Is(err, trajectory.ErrInvalid):
			code = -32602
			message = "Invalid params"
		case errors.Is(err, trajectory.ErrStale):
			code = -32002
			message = err.Error()
		case errors.Is(err, trajectory.ErrNotFound):
			code = -32004
			message = err.Error()
		case errors.Is(err, trajectory.ErrAccess):
			code = -32003
			message = "Trajectory content access denied"
		case errors.Is(err, trajectory.ErrClosed):
			code = -32008
			message = "Trajectory service closed"
		case errors.Is(err, historyfile.ErrStorageBudget):
			code = -32009
			message = err.Error()
		case err.Error() == "method_not_found":
			code = -32601
			message = "Method not found"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			code = -32008
			message = "Query cancelled"
		}
		res := rpcFailure(req.ID, code, message)
		return &res
	}
	return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
}

func (a *trayApp) handleTrajectoryRPC(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	if a.trajectory == nil || a.trajectoryAccess == nil || !a.trajectoryAccess.authorized(r) {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(rpcFailure(nil, -32003, "Trajectory content access denied"))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil {
		w.WriteHeader(413)
		_ = json.NewEncoder(w).Encode(rpcFailure(nil, -32600, "Request size limit"))
		return
	}
	if !json.Valid(raw) {
		_ = json.NewEncoder(w).Encode(rpcFailure(nil, -32700, "Parse error"))
		return
	}
	budget := 6 * time.Second
	if trajectoryCountRequest(raw) {
		budget = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), budget)
	defer cancel()
	stopShutdownCancel := context.AfterFunc(a.refreshContext(), cancel)
	defer stopShutdownCancel()
	var result any
	trim := bytes.TrimSpace(raw)
	if len(trim) > 0 && trim[0] == '[' {
		var batch []json.RawMessage
		_ = json.Unmarshal(trim, &batch)
		if len(batch) == 0 || len(batch) > 16 {
			result = rpcFailure(nil, -32600, "Invalid Request")
		} else {
			responses := []rpcResponse{}
			for _, entry := range batch {
				if response := a.rpcOne(ctx, entry, true); response != nil {
					responses = append(responses, *response)
				}
			}
			if len(responses) > 0 {
				result = responses
			}
		}
	} else {
		result = a.rpcOne(ctx, trim, false)
		if result == (*rpcResponse)(nil) {
			result = nil
		}
	}
	if result == nil {
		w.WriteHeader(204)
		return
	}
	// Access may have been revoked while a disk query was running.
	if !a.trajectoryAccess.isEnabled() {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(rpcFailure(nil, -32003, "Trajectory content access denied"))
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
