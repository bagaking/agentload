package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTrajectoryIndexOpeningPreparedStoreDoesNotRewriteIt(t *testing.T) {
	s, _ := fixture(t, request("prepared evidence"))
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "prepared"})
	s.temporary = false // persist this fixture across the explicit restart
	path, provider := s.path, s.provider
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened := NewPersistent(provider, path)
	if err := reopened.openIndex(); err != nil {
		t.Fatal(err)
	}
	if !reopened.checkpointsReady {
		t.Fatal("lost prepared checkpoint layout")
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("opening an initialized store performed a write", err)
	}
}

type countingDecoder struct{ calls atomic.Int64 }

func (d *countingDecoder) Decode(b []byte, c DecodeContext) ([]snapshot.TrajectoryEvent, error) {
	d.calls.Add(1)
	return (CodexDecoder{}).Decode(b, c)
}
func indexFixture(t *testing.T, body string) (*Service, string, string, *countingDecoder, *bool) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	db := filepath.Join(root, "private", "trajectory.sqlite")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	decoder := &countingDecoder{}
	visible := true
	s := NewPersistent(func(context.Context) SourceSet {
		set := SourceSet{Coverage: coverage("fixture")}
		if visible {
			set.Sources = []Source{{Agent: "codex", Path: path, NativeID: "native", Decoder: decoder}}
		}
		return set
	}, db)
	t.Cleanup(func() { _ = s.Close() })
	return s, path, db, decoder, &visible
}
func appendFile(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.WriteString(body); err != nil {
		t.Fatal(err)
	}
}
func queryOne(t *testing.T, s *Service) snapshot.TrajectoryQueryResult {
	t.Helper()
	// These storage fixtures consume actual event references, not a metadata
	// catalog. Request the explicit evidence/count path under its current contract.
	return searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Count: true})
}

func TestTrajectoryIndexIncrementalAndRestart(t *testing.T) {
	s, path, db, d, visible := indexFixture(t, request("first"))
	q := queryOne(t, s)
	id := q.Sessions[0].MatchedIDs[0]
	session := q.Sessions[0].ID
	checkpointBefore, _, err := s.store.checkpoint(context.Background(), sourceID(s.provider(context.Background()).Sources[0]))
	if err != nil {
		t.Fatal(err)
	}
	_ = queryOne(t, s)
	checkpointAfter, _, err := s.store.checkpoint(context.Background(), sourceID(s.provider(context.Background()).Sources[0]))
	if err != nil || checkpointAfter.Offset != checkpointBefore.Offset || checkpointAfter.Line != checkpointBefore.Line || checkpointAfter.EventCount != checkpointBefore.EventCount || checkpointAfter.Generation != checkpointBefore.Generation {
		t.Fatal("unchanged source advanced the durable parser checkpoint", err)
	}
	body := request("second")
	half := len(body) / 2
	appendFile(t, path, body[:half])
	partial := queryOne(t, s)
	if partial.Coverage.Complete || partial.Sessions[0].EventCount != 1 {
		t.Fatal("partial record became an event")
	}
	appendFile(t, path, body[half:])
	full := queryOne(t, s)
	if full.Sessions[0].EventCount != 2 || full.Sessions[0].ID != session || !full.Coverage.Complete {
		t.Fatalf("bad append: %+v", full)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	resumed := NewPersistent(func(context.Context) SourceSet {
		set := SourceSet{Coverage: coverage("fixture")}
		if *visible {
			set.Sources = []Source{{Agent: "codex", Path: path, NativeID: "native", Decoder: d}}
		}
		return set
	}, db)
	defer resumed.Close()
	next := queryOne(t, resumed)
	if next.Sessions[0].ID != session || next.Sessions[0].EventCount != 2 || next.Sessions[0].MatchedIDs[0] != id {
		t.Fatal("restart lost generation/checkpoint")
	}
	if _, err := resumed.Get(context.Background(), snapshot.TrajectoryGetParams{ID: id}); err != nil {
		t.Fatal("stable locator lost", err)
	}
	info, err := os.Stat(db)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("index not private")
	}
}
func TestTrajectorySourceLifecycleReplacementTruncationAndRemoval(t *testing.T) {
	for _, mode := range []string{"replace", "truncate", "remove_root", "delete"} {
		t.Run(mode, func(t *testing.T) {
			s, path, _, _, visible := indexFixture(t, request("first")+request("second"))
			old := queryOne(t, s).Sessions[0].MatchedIDs[0]
			switch mode {
			case "replace":
				replacement := path + ".new"
				_ = os.WriteFile(replacement, []byte(request("replacement")), 0600)
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			case "truncate":
				if err := os.WriteFile(path, []byte(request("short")), 0600); err != nil {
					t.Fatal(err)
				}
			case "remove_root":
				*visible = false
			case "delete":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				*visible = false
			}
			q := queryOne(t, s)
			_, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: old})
			if mode == "replace" || mode == "truncate" {
				if !errors.Is(err, ErrStale) {
					t.Fatal("old reference reused", err)
				}
			} else {
				if len(q.Sessions) != 0 || !errors.Is(err, ErrNotFound) {
					t.Fatal("removed source leaked", err)
				}
				var remaining int
				if err := s.store.db.QueryRow("SELECT count(*) FROM sources WHERE active=1 AND missing=0").Scan(&remaining); err != nil || remaining != 0 {
					t.Fatal("removed source remained accessible", remaining, err)
				}
				cp, ok, err := s.store.checkpoint(context.Background(), strings.Split(old, ".")[1])
				if err != nil || !ok || !cp.Missing {
					t.Fatal("missing marker absent", err)
				}

			}
		})
	}
}
func TestTrajectoryIndexDamagedCheckpointPreservesRecoveryState(t *testing.T) {
	s, _, _, _, _ := indexFixture(t, request("source still authoritative"))
	old := queryOne(t, s).Sessions[0].MatchedIDs[0]
	if _, err := s.store.db.Exec("UPDATE sources SET checkpoint=? WHERE id=? AND active=1", []byte("{broken"), strings.Split(old, ".")[1]); err != nil {
		t.Fatal(err)
	}

	q, err := s.Query(context.Background(), snapshot.TrajectorySelector{})
	if err == nil && (q.Coverage.Complete || len(q.Sessions) != 0) {
		t.Fatal("damaged checkpoint fabricated readable evidence", q)
	}
	var saved []byte
	if err = s.store.db.QueryRow("SELECT checkpoint FROM sources WHERE id=? AND active=1", strings.Split(old, ".")[1]).Scan(&saved); err != nil || string(saved) != "{broken" {
		t.Fatal("damaged recovery state was overwritten", err)
	}
	if _, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: old}); err == nil {
		t.Fatal("damaged source remained readable")
	}
}
func TestTrajectoryBoundedRetentionHistoryPaginationAndHugeLine(t *testing.T) {
	s, _, _, _, _ := indexFixture(t, strings.Repeat(request("historical request"), 3000))
	q := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Collection: "events", Limit: 2})
	var err error
	if err != nil || len(q.Events) != 2 || q.Next == "" {
		t.Fatal(q, err)
	}
	for page := 0; page < 4; page++ {
		q, err = s.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events", Limit: 2, Cursor: q.Next})
		if err != nil || len(q.Events) != 2 || q.Events[0].Source.Line != 3+page*2 {
			t.Fatal("pagination skipped history", q, err)
		}
	}
	last, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: q.Events[0].SessionID, Around: 0})
	if err != nil || len(last.Events) != 1 || last.Events[0].Source.Line != 3000 {
		t.Fatal("tail not bounded", last, err)
	}
	encoded, _ := json.Marshal(last)
	if len(encoded) > MaxSliceBytes {
		t.Fatal("oversized slice")
	}
	huge, _, _, _, _ := indexFixture(t, request(strings.Repeat("x", 4*maxRecordBytes))+request("after oversized record"))
	result := queryOne(t, huge)
	if result.Coverage.Complete || len(result.Sessions) != 1 || result.Sessions[0].EventCount != 1 || result.Sessions[0].Title != "after oversized record" {
		t.Fatal("huge record blocks later truth", result)
	}
}
func TestTrajectoryIndexRawByteContinuation(t *testing.T) {
	body := request(strings.Repeat("large evidence ", 600))
	s, _, _, _, _ := indexFixture(t, body)
	id := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Count: true}).Sessions[0].MatchedIDs[0]
	offset := 0
	var raw []byte
	for {
		read, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: id, View: "raw", RawOffset: offset, MaxBytes: 2048})
		if err != nil || read.RawChunk == nil {
			t.Fatal(read, err)
		}
		encoded, _ := json.Marshal(read)
		if len(encoded) > 2048 {
			t.Fatal("raw chunk exceeds budget")
		}
		bytes, err := base64.StdEncoding.DecodeString(read.RawChunk.Data)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, bytes...)
		if read.RawChunk.NextOffset == nil {
			break
		}
		if *read.RawChunk.NextOffset <= offset {
			t.Fatal("raw continuation made no progress")
		}
		offset = *read.RawChunk.NextOffset
	}
	if string(raw) != body {
		t.Fatal("continuation changed physical evidence")
	}
}
func TestTrajectoryIndexRevocationPreservesPrivateStore(t *testing.T) {
	s, _, db, _, _ := indexFixture(t, request("private content"))
	_ = queryOne(t, s)
	before, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(db)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("revocation changed useful private evidence", err)
	}
}
