package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

func plainCatalogSelector(q snapshot.TrajectorySelector) bool {
	return q.Text == "" && q.Tool == "" && q.Skill == "" && q.Kind == "" && q.Role == "" && q.ActorID == "" && q.ActorKind == "" && q.EntityKind == "" && q.EntityID == "" && q.Predicate == "" && q.ContextID == ""
}

func cachedSession(st *sourceState) snapshot.TrajectorySession {
	c := st.checkpoint
	native := c.SessionNativeID
	if !c.NativeIDPresent {
		native = st.NativeID
	}
	title := c.SessionTitle
	if title == "" {
		title = st.Agent + " · " + native
	}
	tools := append([]string{}, c.Tools...)
	sort.Strings(tools)
	out := snapshot.TrajectorySession{ID: sessionID(st), NativeID: native, Agent: st.Agent, Title: title, LastAction: c.LastAction, LastEvent: c.LastEvent, EventCount: c.EventCount, MatchedIDs: []string{}, Tools: tools, Coverage: c.Coverage}
	if len(out.NativeID) > 256 {
		out.NativeID = ""
		gap(&out.Coverage, "native_id_omitted_for_size")
	}
	return out
}

// Browsing and fetching a session's metadata do not parse all of its events.
// Only the selected page's bounded source references are materialized.
func (s *Service) querySessionCatalog(ctx context.Context, q snapshot.TrajectorySelector, states []*sourceState, cov snapshot.TrajectoryCoverage) (snapshot.TrajectoryQueryResult, error) {
	out := snapshot.TrajectoryQueryResult{Sessions: []snapshot.TrajectorySession{}, Events: []snapshot.TrajectoryEvent{}, Coverage: cov, Revision: sourceRevision(states), WatchCursor: s.watchCursorLocked(q)}
	pagination := q
	pagination.Cursor, pagination.Limit = "", 0
	selector, _ := json.Marshal(pagination)
	prefix := "query." + out.Revision + "." + digest(selector)
	start := 0
	if q.Cursor != "" {
		parts := strings.Split(q.Cursor, ":")
		if len(parts) != 2 || parts[0] != prefix {
			return out, ErrStale
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 0 {
			return out, ErrInvalid
		}
		start = n
	}
	matched := 0
	for _, st := range states {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if st.checkpoint.EventCount == 0 || (q.Agent != "" && q.Agent != st.Agent) || (q.SessionID != "" && q.SessionID != sessionID(st)) {
			continue
		}
		if matched >= start && len(out.Sessions) < q.Limit {
			summary := cachedSession(st)
			summary.MatchedCount = st.checkpoint.EventCount
			facts, err := s.store.take(ctx, st, -1, -1, false, false, 50)
			for _, fact := range facts {
				summary.MatchedIDs = append(summary.MatchedIDs, fact.event.ID)
			}

			if err != nil {
				if q.Count || ctx.Err() != nil {
					return out, err
				}
				gap(&out.Coverage, "index_read_failed")
				continue
			}
			if summary.MatchedCount > len(summary.MatchedIDs) {
				gap(&summary.Coverage, "matched_reference_limit")
			}
			mergeCoverage(&out.Coverage, summary.Coverage)
			out.Sessions = append(out.Sessions, summary)
		}
		matched++
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if q.Count {
		out.MatchedTotal = &matched
	}
	return out, boundQueryPage(&out, prefix, start, matched)
}
