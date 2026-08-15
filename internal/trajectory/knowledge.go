package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

var annotationBucket = []byte("annotations-v1")

const maxKnowledgeRecords = 1024
const maxKnowledgeRecordBytes = 4096
const maxKnowledgeQueryBytes = 64 * 1024

type annotationMeta struct {
	Version  int    `json:"version"`
	Revision uint64 `json:"revision"`
	Count    int    `json:"count"`
}

type knowledgeSourceCheck struct {
	status         string
	matches        bool
	entityComplete bool
	sourceComplete bool
}

func knowledgeKind(kind string) bool {
	return kind == "observation" || kind == "candidate" || kind == "verification" || kind == "counterexample"
}

func knowledgeID(id string) bool {
	if !strings.HasPrefix(id, "k.") || len(id) != 34 {
		return false
	}
	_, err := hex.DecodeString(id[2:])
	return err == nil
}

func ValidateAnnotationParams(p snapshot.TrajectoryAnnotationParams) error {
	switch p.Operation {
	case "create":
		if p.ID != "" || !knowledgeKind(p.Kind) || len(p.SourceIDs) < 1 || len(p.SourceIDs) > 8 || strings.TrimSpace(p.Text) == "" || len(p.Text) > 2048 || !utf8.ValidString(p.Text) {
			return ErrInvalid
		}
		if p.Kind == "verification" || p.Kind == "counterexample" {
			if !knowledgeID(p.TargetID) {
				return fmt.Errorf("%w: verification and counterexample require a candidate target", ErrInvalid)
			}
		} else if p.TargetID != "" {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for _, id := range p.SourceIDs {
			if _, _, _, err := knowledgeEventLocator(id); err != nil || seen[id] {
				return fmt.Errorf("%w: invalid or repeated source event", ErrInvalid)
			}
			seen[id] = true
		}
		return validateKnowledgeApplicability(p.Applicability)
	case "withdraw", "delete":
		if !knowledgeID(p.ID) || p.Kind != "" || len(p.SourceIDs) > 0 || p.Text != "" || p.TargetID != "" || p.Applicability != nil {
			return ErrInvalid
		}
	default:
		return fmt.Errorf("%w: explicit annotation operation required", ErrInvalid)
	}
	return nil
}

func validateKnowledgeApplicability(a *snapshot.TrajectoryKnowledgeApplicability) error {
	if a == nil {
		return nil
	}
	if len(a.Version) > 256 || !utf8.ValidString(a.Version) {
		return ErrInvalid
	}
	for _, values := range []map[string]string{a.Configuration, a.Environment} {
		if len(values) > 8 {
			return ErrInvalid
		}
		for key, value := range values {
			if strings.TrimSpace(key) == "" || len(key) > 128 || len(value) > 256 || !utf8.ValidString(key) || !utf8.ValidString(value) {
				return ErrInvalid
			}
		}
	}
	for _, refs := range [][]string{a.EvaluationRefs, a.ArtifactRefs} {
		if len(refs) > 4 {
			return ErrInvalid
		}
		for _, ref := range refs {
			if strings.TrimSpace(ref) == "" || len(ref) > 512 || !utf8.ValidString(ref) {
				return ErrInvalid
			}
		}
	}
	return nil
}

func knowledgeScope(a *snapshot.TrajectoryKnowledgeApplicability) string {
	if a == nil || len(a.Configuration) == 0 && len(a.Environment) == 0 && a.Version == "" && len(a.EvaluationRefs) == 0 && len(a.ArtifactRefs) == 0 {
		return "unprovided"
	}
	if len(a.Configuration) > 0 && len(a.Environment) > 0 && a.Version != "" && len(a.EvaluationRefs) > 0 && len(a.ArtifactRefs) > 0 {
		return "provided"
	}
	return "partial"
}

func annotationMetadata(bucket *bolt.Bucket) (annotationMeta, error) {
	if bucket == nil {
		return annotationMeta{Version: 1}, nil
	}
	raw := bucket.Get(metaKey)
	if len(raw) == 0 {
		if key, _ := bucket.Cursor().First(); key != nil {
			return annotationMeta{}, errors.New("annotation metadata missing")
		}
		return annotationMeta{Version: 1}, nil
	}
	var meta annotationMeta
	if err := json.Unmarshal(raw, &meta); err != nil || meta.Version != 1 || meta.Count < 0 || meta.Count > maxKnowledgeRecords {
		return meta, errors.New("annotation metadata invalid")
	}
	return meta, nil
}

func annotationRecord(bucket *bolt.Bucket, id string) (snapshot.TrajectoryKnowledge, error) {
	var record snapshot.TrajectoryKnowledge
	if bucket == nil {
		return record, ErrNotFound
	}
	raw := bucket.Get([]byte(id))
	if len(raw) == 0 {
		return record, ErrNotFound
	}
	if len(raw) > maxKnowledgeRecordBytes || json.Unmarshal(raw, &record) != nil || record.ID != id || !knowledgeKind(record.Kind) || record.State != "active" && record.State != "withdrawn" {
		return record, errors.New("annotation record invalid")
	}
	return record, nil
}

// Authored notes are not a rebuildable source projection. They remain private
// and inaccessible when content access is disabled, and require explicit delete.
func (s *Service) openAnnotations() error {
	if s.annotationDB != nil {
		return nil
	}
	if s.path == "" {
		return errors.New("annotation storage path unavailable")
	}
	path := filepath.Join(filepath.Dir(s.path), "annotations.bbolt")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 200 * time.Millisecond})
	if err != nil {
		return err
	}
	if err = os.Chmod(path, 0600); err == nil {
		err = db.View(func(tx *bolt.Tx) error {
			_, err := annotationMetadata(tx.Bucket(annotationBucket))
			return err
		})
	}
	if err != nil {
		_ = db.Close()
		return err
	}
	s.annotationDB = db
	return nil
}

func (s *Service) knowledgeCollect(ctx context.Context) ([]*sourceState, map[string]Source, snapshot.TrajectoryCoverage, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, snapshot.TrajectoryCoverage{}, err
	}
	set := s.provider(ctx)
	if accessDisabled(set.Coverage) {
		return nil, nil, set.Coverage, ErrAccess
	}
	allowed := map[string]Source{}
	for _, source := range set.Sources {
		if source.Decoder != nil {
			allowed[sourceID(source)] = source
		}
	}
	states, cov := s.collectSet(ctx, set)
	if accessDisabled(cov) {
		return nil, nil, cov, ErrAccess
	}
	if err := s.openAnnotations(); err != nil {
		return nil, nil, cov, err
	}
	return states, allowed, cov, ctx.Err()
}

func knowledgeEventLocator(id string) (string, int64, int, error) {
	parts := strings.Split(id, ".")
	if len(parts) != 6 || parts[0] != "e" || len(id) > 256 {
		return "", 0, 0, ErrInvalid
	}
	for _, value := range []string{parts[1], parts[2], parts[5]} {
		if len(value) != 16 {
			return "", 0, 0, ErrInvalid
		}
		if _, err := hex.DecodeString(value); err != nil {
			return "", 0, 0, ErrInvalid
		}
	}
	offset, err := strconv.ParseInt(parts[3], 36, 64)
	if err != nil || offset < 0 {
		return "", 0, 0, ErrInvalid
	}
	block, err := strconv.Atoi(parts[4])
	if err != nil || block < 0 || block >= 64 {
		return "", 0, 0, ErrInvalid
	}
	return parts[1], offset, block, nil
}

func knowledgeState(states []*sourceState, id string) *sourceState {
	for _, state := range states {
		if state.ID == id {
			return state
		}
	}
	return nil
}

func (s *Service) knowledgeEvent(st *sourceState, id string) (snapshot.TrajectoryEvent, error) {
	var event snapshot.TrajectoryEvent
	_, offset, block, err := knowledgeEventLocator(id)
	if err != nil {
		return event, err
	}
	if st.Generation != strings.Split(id, ".")[2] {
		return event, ErrStale
	}
	if s.store == nil {
		return event, errors.New("source index unavailable")
	}
	event, err = s.store.event(context.Background(), st, offset, block)
	if err != nil {
		return event, err
	}
	if event.ID != id || event.Source.ID != st.ID || event.Source.Generation != st.Generation {
		return event, ErrStale
	}
	_, err = readRecord(st, event.Source)
	return event, err
}

func (s *Service) Annotate(ctx context.Context, p snapshot.TrajectoryAnnotationParams) (snapshot.TrajectoryAnnotationResult, error) {
	out := snapshot.TrajectoryAnnotationResult{}
	if err := ValidateAnnotationParams(p); err != nil {
		return out, err
	}
	if err := s.lockOperation(ctx); err != nil {
		return snapshot.TrajectoryAnnotationResult{}, err
	}
	defer s.opMu.Unlock()
	states, allowed, cov, err := s.knowledgeCollect(ctx)
	if err != nil {
		return out, err
	}
	derived := map[string]snapshot.TrajectoryKnowledge{}
	if p.TargetID != "" {
		records, err := s.deriveExperiences(ctx, states, &cov)
		if err != nil {
			return out, err
		}
		for _, record := range records {
			derived[record.ID] = record
		}
	}
	err = s.annotationDB.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(annotationBucket)
		if err != nil {
			return err
		}
		meta, err := annotationMetadata(bucket)
		if err != nil {
			return err
		}
		var record snapshot.TrajectoryKnowledge
		if p.Operation == "create" {
			if meta.Count >= maxKnowledgeRecords {
				return fmt.Errorf("%w: annotation record limit", ErrInvalid)
			}
			id := make([]byte, 16)
			if _, err := rand.Read(id); err != nil {
				return err
			}
			now := time.Now().UTC()
			record = snapshot.TrajectoryKnowledge{ID: "k." + hex.EncodeToString(id), Kind: p.Kind, State: "active", Origin: "annotation", Text: p.Text, Applicability: p.Applicability, ScopeStatus: knowledgeScope(p.Applicability), EvidenceState: "valid", CreatedAt: &now, UpdatedAt: &now, Sources: []snapshot.TrajectoryKnowledgeSource{}}
			for _, id := range p.SourceIDs {
				sourceID, _, _, _ := knowledgeEventLocator(id)
				st := knowledgeState(states, sourceID)
				if st == nil {
					return ErrNotFound
				}
				event, err := s.knowledgeEvent(st, id)
				if err != nil {
					return err
				}
				record.Sources = append(record.Sources, snapshot.TrajectoryKnowledgeSource{EventID: id, SessionID: event.SessionID, Agent: st.Agent, Source: event.Source, Status: "valid"})
			}
			if p.TargetID != "" {
				target, err := annotationRecord(bucket, p.TargetID)
				if errors.Is(err, ErrNotFound) {
					if generated, exists := derived[p.TargetID]; exists {
						target, err = generated, nil
					}
				}
				if err != nil {
					return err
				}
				if target.Kind != "candidate" || target.State != "active" {
					return fmt.Errorf("%w: active candidate target required", ErrInvalid)
				}
				kind := "verification_of"
				if p.Kind == "counterexample" {
					kind = "counterexample_to"
				}
				record.Links = []snapshot.TrajectoryKnowledgeLink{{Kind: kind, TargetID: p.TargetID, Status: "active"}}
			}
			meta.Count++
		} else {
			record, err = annotationRecord(bucket, p.ID)
			if err != nil {
				return err
			}
			if p.Operation == "delete" {
				if err := bucket.Delete([]byte(p.ID)); err != nil {
					return err
				}
				meta.Count--
				out.DeletedID = p.ID
			} else {
				if record.State == "withdrawn" {
					out.Record = &record
					out.Revision = strconv.FormatUint(meta.Revision, 10)
					return nil
				}
				record.State = "withdrawn"
				now := time.Now().UTC()
				record.UpdatedAt = &now
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.Operation != "delete" {
			encoded, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if len(encoded) > maxKnowledgeRecordBytes {
				return fmt.Errorf("%w: annotation byte limit", ErrInvalid)
			}
			if err := bucket.Put([]byte(record.ID), encoded); err != nil {
				return err
			}
			out.Record = &record
		}
		meta.Revision++
		out.Revision = strconv.FormatUint(meta.Revision, 10)
		return putJSON(bucket, metaKey, meta)
	})
	if err != nil {
		return snapshot.TrajectoryAnnotationResult{}, err
	}
	if out.Record != nil {
		err = s.annotationDB.View(func(tx *bolt.Tx) error {
			cov := coverage("annotation source references")
			record, _ := s.hydrateKnowledge(ctx, tx, states, allowed, *out.Record, snapshot.TrajectorySelector{}, map[string]knowledgeSourceCheck{}, derived, &cov)
			out.Record = &record
			return nil
		})
	}
	return out, err
}

func knowledgeSelector(q snapshot.TrajectorySelector) (snapshot.TrajectorySelector, error) {
	if q.Collection == "" {
		q.Collection = "knowledge"
	}
	if q.Collection != "knowledge" || q.Kind != "" && !knowledgeKind(q.Kind) || q.State != "" && q.State != "active" && q.State != "withdrawn" || q.ContextID != "" || q.ContextScope != "" || q.RelationKind != "" || q.ActorID != "" || q.ActorKind != "" || q.Role != "" {
		return q, ErrInvalid
	}
	base := q
	base.Collection, base.Kind, base.State = "events", "", ""
	if err := ValidateSelector(&base); err != nil {
		return q, err
	}
	q.Limit = base.Limit
	return q, nil
}

func knowledgeRevision(states []*sourceState, meta annotationMeta, q snapshot.TrajectorySelector) string {
	q.Cursor, q.Limit = "", 0
	encoded, _ := json.Marshal(q)
	return digest([]byte(sourceRevision(states) + ":" + strconv.FormatUint(meta.Revision, 10) + ":" + experienceRuleID + ":" + experienceRuleVersion + ":" + string(encoded)))
}

func (s *Service) knowledgeSourceStatus(ctx context.Context, states []*sourceState, allowed map[string]Source, source snapshot.TrajectoryKnowledgeSource, q snapshot.TrajectorySelector) knowledgeSourceCheck {
	if ctx.Err() != nil {
		return knowledgeSourceCheck{status: "unavailable"}
	}
	id, _, _, err := knowledgeEventLocator(source.EventID)
	if err != nil || source.Source.ID != id {
		return knowledgeSourceCheck{status: "unavailable"}
	}
	st := knowledgeState(states, id)
	if st == nil {
		if authorized, ok := allowed[id]; ok {
			if _, err := os.Stat(authorized.Path); os.IsNotExist(err) {
				return knowledgeSourceCheck{status: "missing"}
			}
		}
		return knowledgeSourceCheck{status: "unavailable"}
	}
	event, err := s.knowledgeEvent(st, source.EventID)
	if err != nil {
		if errors.Is(err, ErrStale) {
			return knowledgeSourceCheck{status: "stale"}
		}
		if errors.Is(err, ErrNotFound) {
			return knowledgeSourceCheck{status: "missing"}
		}
		return knowledgeSourceCheck{status: "unavailable"}
	}
	entity := q
	entity.Text, entity.Kind, entity.State, entity.Collection, entity.Agent, entity.SessionID = "", "", "", "events", "", ""
	return knowledgeSourceCheck{status: "valid", matches: matches(event, entity), entityComplete: event.EntityCoverage == nil || event.EntityCoverage.Complete, sourceComplete: st.checkpoint.Coverage.Complete}
}

func (s *Service) hydrateKnowledge(ctx context.Context, tx *bolt.Tx, states []*sourceState, allowed map[string]Source, record snapshot.TrajectoryKnowledge, q snapshot.TrajectorySelector, cache map[string]knowledgeSourceCheck, derived map[string]snapshot.TrajectoryKnowledge, cov *snapshot.TrajectoryCoverage) (snapshot.TrajectoryKnowledge, bool) {
	valid, matched, sourceMatched := 0, false, q.Agent == "" && q.SessionID == ""
	statuses := map[string]bool{}
	for i := range record.Sources {
		source := &record.Sources[i]
		if (q.Agent == "" || q.Agent == source.Agent) && (q.SessionID == "" || q.SessionID == source.SessionID) {
			sourceMatched = true
		}
		check, ok := cache[source.EventID]
		if !ok {
			check = s.knowledgeSourceStatus(ctx, states, allowed, *source, q)
			if len(cache) < 256 {
				cache[source.EventID] = check
			}
		}
		source.Status = check.status
		statuses[check.status] = true
		if check.status == "valid" {
			valid++
			if !check.sourceComplete {
				gap(cov, "knowledge_source_coverage_incomplete")
			}
			if (q.Skill != "" || q.EntityKind != "" || q.EntityID != "" || q.Predicate != "") && !check.entityComplete {
				gap(cov, "knowledge_entity_coverage_incomplete")
			}
			if check.matches && (q.Agent == "" || q.Agent == source.Agent) && (q.SessionID == "" || q.SessionID == source.SessionID) {
				matched = true
			}
		} else {
			gap(cov, "knowledge_source_"+check.status)
		}
	}
	switch {
	case valid == len(record.Sources) && valid > 0:
		record.EvidenceState = "valid"
	case valid > 0:
		record.EvidenceState = "partial"
	case len(statuses) == 1 && statuses["stale"]:
		record.EvidenceState = "stale"
	case len(statuses) == 1 && statuses["missing"]:
		record.EvidenceState = "missing"
	default:
		record.EvidenceState = "unavailable"
	}
	for i := range record.Links {
		target, err := annotationRecord(tx.Bucket(annotationBucket), record.Links[i].TargetID)
		if errors.Is(err, ErrNotFound) {
			if generated, exists := derived[record.Links[i].TargetID]; exists {
				target, err = generated, nil
			}
		}
		if err == nil {
			record.Links[i].Status = target.State
		} else {
			record.Links[i].Status = "missing"
			gap(cov, "knowledge_target_missing")
		}
	}
	needsEntity := q.Tool != "" || q.Skill != "" || q.EntityKind != "" || q.EntityID != "" || q.Predicate != ""
	return record, sourceMatched && (!needsEntity || matched) && (record.Origin != "deterministic" || record.EvidenceState == "valid")
}

func (s *Service) QueryKnowledge(ctx context.Context, q snapshot.TrajectorySelector) (snapshot.TrajectoryQueryResult, error) {
	out := snapshot.TrajectoryQueryResult{Sessions: []snapshot.TrajectorySession{}, Events: []snapshot.TrajectoryEvent{}, Knowledge: []snapshot.TrajectoryKnowledge{}, Coverage: coverage("local annotations and current authorized source references")}
	var err error
	q, err = knowledgeSelector(q)
	if err != nil {
		return out, err
	}
	if err := s.lockOperation(ctx); err != nil {
		return snapshot.TrajectoryQueryResult{}, err
	}
	defer s.opMu.Unlock()
	states, allowed, sourceCoverage, err := s.knowledgeCollect(ctx)
	if err != nil {
		return out, err
	}
	mergeCoverage(&out.Coverage, sourceCoverage)
	experiences, err := s.deriveExperiences(ctx, states, &out.Coverage)
	if err != nil {
		return out, err
	}
	derived := map[string]snapshot.TrajectoryKnowledge{}
	for _, record := range experiences {
		derived[record.ID] = record
	}
	err = s.annotationDB.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(annotationBucket)
		meta, err := annotationMetadata(bucket)
		if err != nil {
			return err
		}
		out.Revision = knowledgeRevision(states, meta, q)
		start, err := knowledgeCursorStart(q.Cursor, out.Revision)
		if err != nil {
			return err
		}
		matched := 0
		cache := map[string]knowledgeSourceCheck{}
		records := append([]snapshot.TrajectoryKnowledge{}, experiences...)
		if bucket != nil {
			cursor := bucket.Cursor()
			for key, _ := cursor.Seek([]byte("k.")); key != nil && strings.HasPrefix(string(key), "k."); key, _ = cursor.Next() {
				if len(records) >= maxKnowledgeRecords+maxExperienceCandidates {
					gap(&out.Coverage, "knowledge_record_limit")
					break
				}
				record, err := annotationRecord(bucket, string(key))
				if err != nil {
					gap(&out.Coverage, "knowledge_record_unreadable")
					continue
				}
				records = append(records, record)
			}
		}
		sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
		for _, record := range records {
			if err := ctx.Err(); err != nil {
				return err
			}
			if q.Kind != "" && record.Kind != q.Kind || q.State != "" && record.State != q.State || !s.knowledgeRecordTextMatches(ctx, states, record, q.Text) {
				continue
			}
			record, include := s.hydrateKnowledge(ctx, tx, states, allowed, record, q, cache, derived, &out.Coverage)
			if !include {
				continue
			}
			if matched >= start && len(out.Knowledge) < q.Limit {
				if len(record.Text) > 320 {
					record.Text = shorten(record.Text, 320)
					record.Omissions = append(record.Omissions, "knowledge_text_preview")
				}
				out.Knowledge = append(out.Knowledge, record)
				if encoded, _ := json.Marshal(out); len(encoded) > maxKnowledgeQueryBytes-256 {
					out.Knowledge = out.Knowledge[:len(out.Knowledge)-1]
					gap(&out.Coverage, "knowledge_result_byte_limit")
					out.Next = fmt.Sprintf("%s:%d", out.Revision, matched)
					break
				}
			} else if matched >= start+len(out.Knowledge) {
				out.Next = fmt.Sprintf("%s:%d", out.Revision, matched)
				break
			}
			matched++
		}
		return nil
	})
	return out, err
}

func knowledgeCursorStart(cursor, revision string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	parts := strings.Split(cursor, ":")
	if len(parts) != 2 || parts[0] != revision {
		return 0, ErrStale
	}
	start, err := strconv.Atoi(parts[1])
	if err != nil || start < 0 || start > maxKnowledgeRecords+maxExperienceCandidates {
		return 0, ErrInvalid
	}
	return start, nil
}

func knowledgeTextMatches(text, query string) bool {
	text = strings.ToLower(text)
	for _, word := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}

func (s *Service) knowledgeRecordTextMatches(ctx context.Context, states []*sourceState, record snapshot.TrajectoryKnowledge, text string) bool {
	if knowledgeTextMatches(record.Text, text) {
		return true
	}
	if record.Origin != "deterministic" {
		return false
	}
	for _, source := range record.Sources {
		if ctx.Err() != nil {
			return false
		}
		st := knowledgeState(states, source.Source.ID)
		if st == nil {
			continue
		}
		event, err := s.knowledgeEvent(st, source.EventID)
		if err == nil && matches(event, snapshot.TrajectorySelector{Text: text}) {
			return true
		}
	}
	return false
}

func (s *Service) GetKnowledge(ctx context.Context, p snapshot.TrajectoryGetParams) (snapshot.TrajectoryGetResult, error) {
	out := snapshot.TrajectoryGetResult{FocusID: p.ID, Events: []snapshot.TrajectoryEvent{}, ByteLimit: MaxSliceBytes, Coverage: coverage("local annotation and current authorized source references")}
	if !knowledgeID(p.ID) || p.Raw || p.RawOffset != 0 || p.MemberOffset != 0 || p.Around < 0 || p.Around > 5 || p.MaxBytes < 0 || p.MaxBytes > MaxSliceBytes || p.View != "" && p.View != "slice" && p.View != "details" {
		return out, ErrInvalid
	}
	if p.MaxBytes > 0 {
		out.ByteLimit = p.MaxBytes
	}
	if out.ByteLimit < 1024 {
		return out, ErrInvalid
	}
	if err := s.lockOperation(ctx); err != nil {
		return snapshot.TrajectoryGetResult{}, err
	}
	defer s.opMu.Unlock()
	states, allowed, sourceCoverage, err := s.knowledgeCollect(ctx)
	if err != nil {
		return out, err
	}
	mergeCoverage(&out.Coverage, sourceCoverage)
	experiences, err := s.deriveExperiences(ctx, states, &out.Coverage)
	if err != nil {
		return out, err
	}
	derived := map[string]snapshot.TrajectoryKnowledge{}
	for _, record := range experiences {
		derived[record.ID] = record
	}
	err = s.annotationDB.View(func(tx *bolt.Tx) error {
		if _, err := annotationMetadata(tx.Bucket(annotationBucket)); err != nil {
			return err
		}
		record, err := annotationRecord(tx.Bucket(annotationBucket), p.ID)
		if errors.Is(err, ErrNotFound) {
			if generated, exists := derived[p.ID]; exists {
				record, err = generated, nil
			}
		}
		if err != nil {
			return err
		}
		record, _ = s.hydrateKnowledge(ctx, tx, states, allowed, record, snapshot.TrajectorySelector{}, map[string]knowledgeSourceCheck{}, derived, &out.Coverage)
		if record.Origin == "deterministic" && record.EvidenceState != "valid" {
			return ErrStale
		}
		out.Knowledge = &record
		for {
			encoded, _ := json.Marshal(out)
			if len(encoded) <= out.ByteLimit {
				return nil
			}
			if len(record.Text) <= 64 {
				return fmt.Errorf("%w: budget cannot include knowledge provenance and scope", ErrInvalid)
			}
			record.Text = shorten(record.Text, len(record.Text)/2)
			out.Truncated = true
			if len(record.Omissions) == 0 || record.Omissions[len(record.Omissions)-1] != "knowledge_text_truncated_for_budget" {
				record.Omissions = append(record.Omissions, "knowledge_text_truncated_for_budget")
			}
		}
	})
	return out, err
}
