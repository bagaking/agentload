package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTrajectoryCatalogPruneCancellationPreservesSource(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("keep the recorded source"))
	before, err := os.ReadFile(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checked := false
	s.store.checkCapacity = func(string, uint64) error {
		checked = true
		cancel()
		return nil
	}
	_, _ = s.PrepareCatalog(ctx, SourceSet{CatalogComplete: true, Coverage: coverage("withdrawn root")}, func() bool { return true })
	if !checked {
		t.Fatal("fixture did not reach the removal transaction")
	}
	var missing bool
	if err := s.store.db.QueryRow("SELECT missing FROM sources WHERE id=?", st.ID).Scan(&missing); err != nil || missing {
		t.Fatal("cancelled catalog removal committed a source change", missing, err)
	}
	after, err := os.ReadFile(st.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("catalog removal changed the original session", err)
	}
}

func TestTrajectorySourceAuditResumesAndDoesNotBlessRewrite(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request(strings.Repeat("prefix ", 24000))+request("ordinary middle")+request("final anchor"))
	f := newSourceStoreFixture(t)
	chunks := importFixtureFacts(t, f, st, canonicalFixtureFacts(t, s, st))
	if len(chunks) < 2 {
		t.Fatal("fixture lacks multiple audit units")
	}
	if _, err := f.db.Exec("UPDATE sources SET verified_size=-1,verified_mtime=-1"); err != nil {
		t.Fatal(err)
	}
	more, err := f.auditSource(context.Background(), st)
	if err != nil || !more {
		t.Fatal("first unit sealed an incomplete audit", more, err)
	}
	p, err := f.readiness(context.Background())
	if err != nil || p[st.ID].verifiedSize != -1 {
		t.Fatal("incomplete audit moved seal", err)
	}
	path := f.path
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f, err = openSourceStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	for more {
		more, err = f.auditSource(context.Background(), st)
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err = f.readiness(context.Background())
	if err != nil || p[st.ID].verifiedSize != st.Info.Size() || p[st.ID].verifiedMtime != st.Info.ModTime().UnixNano() {
		t.Fatal("resumed audit did not seal current source", err)
	}
	before := p[st.ID]
	body, err := os.ReadFile(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(body), "ordinary", "newmatch", 1) + request("append")
	if err = os.WriteFile(st.Path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(chunks)+1; n++ {
		_, err = f.auditSource(context.Background(), st)
		if err != nil {
			break
		}
	}
	if !errors.Is(err, ErrStale) {
		t.Fatal("rewrite escaped resumed audit", err)
	}
	p, err = f.readiness(context.Background())
	if err != nil || p[st.ID].verifiedSize != before.verifiedSize || p[st.ID].verifiedMtime != before.verifiedMtime {
		t.Fatal("failed audit changed proof seal", err)
	}
}

func TestTrajectorySourceUnsealedNegativeVerifiesAllRawBytes(t *testing.T) {
	d := &countingDecoder{}
	s, st := replayFixture(t, "codex", d, request(strings.Repeat("prefix ", 24000))+request("ordinary middle")+request("final anchor"))
	f := newSourceStoreFixture(t)
	importFixtureFacts(t, f, st, canonicalFixtureFacts(t, s, st))
	if _, err := f.db.Exec("UPDATE sources SET verified_size=-1,verified_mtime=-1"); err != nil {
		t.Fatal(err)
	}
	d.calls.Store(0)
	visits := 0
	q := snapshot.TrajectorySelector{Text: "newmatch"}
	err := f.walkSourceCandidates(context.Background(), st, func(filter *sourceFilter) bool { return filter.maybe(q) }, nil, func(sourceFact) error { visits++; return nil })
	if err != nil || visits != 0 || d.calls.Load() != 0 {
		t.Fatal("unsealed negative decoded irrelevant facts", err, visits, d.calls.Load())
	}
	before, err := os.ReadFile(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(before), "ordinary", "newmatch", 1) + request("append")
	if err = os.WriteFile(st.Path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	err = f.walkSourceCandidates(context.Background(), st, func(filter *sourceFilter) bool { return filter.maybe(q) }, nil, func(sourceFact) error { visits++; return nil })
	if !errors.Is(err, ErrStale) || visits != 0 {
		t.Fatal("middle rewrite plus append escaped negative verification", err, visits)
	}
	ready, err := f.readiness(context.Background())
	if err != nil || ready[st.ID].verifiedSize != -1 {
		t.Fatal("local negative check published a whole-source seal", err)
	}
}

func TestTrajectorySourceRevocationRetainsIrreproducibleEvidence(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("source"))
	facts := canonicalFixtureFacts(t, s, st)
	facts[0].event.Text = "canonical-only evidence"
	importFixtureFacts(t, s.store, st, facts)
	path := s.path
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Reset(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("revocation deleted useful evidence", err)
	}
	s.provider = func(context.Context) SourceSet {
		return SourceSet{Coverage: snapshot.TrajectoryCoverage{Complete: false, Gaps: []string{"content_access_disabled"}}}
	}
	q, err := s.Query(context.Background(), snapshot.TrajectorySelector{Text: "canonical-only"})
	if err != nil || len(q.Sessions) > 0 || !accessDisabled(q.Coverage) {
		t.Fatal("revocation exposed retained facts", q, err)
	}
}

func TestTrajectorySourceMigrationSQLiteResumesAndPreservesCompleteExceptions(t *testing.T) {
	fixtureService, st := replayFixture(t, "codex", CodexDecoder{}, request("source first")+call("cross")+result("cross")+request("source last"))
	facts := canonicalFixtureFacts(t, fixtureService, st)
	facts[0].event.Text = "canonical-only bagakit-researcher"
	facts[0].event.Context = &snapshot.TrajectoryContextEvidence{NativeID: "preserved-context", NativeField: "legacy/context"}
	facts[0].event.Omissions = []string{"original-omission"}
	root := t.TempDir()
	path := filepath.Join(root, "trajectory.sqlite")
	old, err := openFactStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var originalCheckpoint []byte
	err = old.write(context.Background(), func(w *factWriter) error {
		row, err := w.source(st.ID, st.Agent, st.checkpoint)
		if err != nil {
			return err
		}
		for i, fact := range facts {
			if _, err = w.event(row, fact.event, i < 2); err != nil {
				return err
			}
		}
		original, err := json.Marshal(st.checkpoint)
		if err != nil {
			return err
		}
		original = append(original[:len(original)-1], []byte(`,"future_recovery":{"opaque":[1,"retain"]}}`)...)
		originalCheckpoint, err = encodeStored(original)
		if err != nil {
			return err
		}
		if _, err = w.tx.Exec("UPDATE sources SET checkpoint=? WHERE rowid=?", originalCheckpoint, row); err != nil {
			return err
		}
		if _, err = w.tx.Exec("INSERT INTO meta VALUES('opaque-recovery','retain control truth')"); err != nil {
			return err
		}
		_, err = w.tx.Exec("INSERT INTO metadata VALUES(?,?,?,0)", metadataPath([][]byte{[]byte("unknown-recovery")}), "73", []byte("retain unknown recovery body"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = old.db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	input, _, err := migrationFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewPersistent(fixtureService.provider, path)
	if err = s.openIndex(); !errors.Is(err, errStorageMigration) {
		t.Fatal("first batch did not yield", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("partial migration altered canonical original", err)
	}
	s = NewPersistent(fixtureService.provider, path)
	defer s.Close()
	for attempt := 0; attempt < 100; attempt++ {
		err = s.openIndex()
		if err == nil {
			break
		}
		if !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
	}
	if err != nil {
		t.Fatal("migration did not finish", err)
	}
	cp, ok, err := s.store.checkpoint(context.Background(), st.ID)
	if err != nil || !ok || !reflect.DeepEqual(cp, st.checkpoint) {
		t.Fatal("canonical checkpoint changed", err)
	}
	for _, fact := range facts {
		got, err := s.store.event(context.Background(), st, fact.offset, fact.block)
		if err != nil || !reflect.DeepEqual(got, fact.event) {
			t.Fatal("complete canonical fact changed", err)
		}
	}
	var sequence string
	var body []byte
	if err = s.store.db.QueryRow("SELECT sequence,body FROM metadata WHERE path=?", metadataPath([][]byte{[]byte("unknown-recovery")})).Scan(&sequence, &body); err != nil || sequence != "73" || string(body) != "retain unknown recovery body" {
		t.Fatal("metadata changed", err)
	}
	if err = s.store.db.QueryRow("SELECT body FROM metadata WHERE path=?", migrationRecoveryPath(input, "meta", "opaque-recovery")).Scan(&body); err != nil || string(body) != "retain control truth" {
		t.Fatal("unknown control truth lost", err)
	}
	if err = s.store.db.QueryRow("SELECT body FROM metadata WHERE path=?", migrationRecoveryPath(input, "checkpoint", "1")).Scan(&body); err != nil || !bytes.Equal(body, originalCheckpoint) {
		t.Fatal("unknown checkpoint fields lost", err)
	}
	p, err := s.store.readiness(context.Background())
	if err != nil || p[st.ID].count != 2 || p[st.ID].offset != facts[1].offset || p[st.ID].block != facts[1].block {
		t.Fatal("search prefix silently advanced", err)
	}
	q, err := s.querySearch(context.Background(), snapshot.TrajectorySelector{Text: "canonical-only", Count: true}, []*sourceState{st}, coverage("migration fixture"))
	if err != nil || len(q.Sessions) != 1 || q.Sessions[0].MatchedIDs[0] != facts[0].event.ID {
		t.Fatal("exception is not searchable", q, err)
	}
	for _, suffix := range []string{".source-legacy", ".source-migrating"} {
		if _, err = os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatal("verified migration did not retire old structure", suffix, err)
		}
	}
}

func TestTrajectorySourceMigrationMissingSourceKeepsFullFacts(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("useful historical evidence"))
	facts := canonicalFixtureFacts(t, s, st)
	path := filepath.Join(t.TempDir(), "trajectory.sqlite")
	f, err := openFactStore(path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.write(context.Background(), func(w *factWriter) error {
		row, e := w.source(st.ID, st.Agent, st.checkpoint)
		if e != nil {
			return e
		}
		_, e = w.event(row, facts[0].event, true)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	f.db.Close()
	m := NewPersistent(func(context.Context) SourceSet {
		return SourceSet{Coverage: coverage("current empty catalog"), CatalogComplete: true}
	}, path)
	defer m.Close()
	for n := 0; n < 100; n++ {
		err = m.openIndex()
		if err == nil {
			break
		}
		if !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	var frame []byte
	if err = m.store.db.QueryRow("SELECT body FROM exceptions").Scan(&frame); err != nil {
		t.Fatal("missing source fact discarded", err)
	}
	raw, err := decodeSourceValue(frame, maxSourceRangeLogicalBytes)
	if err != nil {
		t.Fatal(err)
	}
	var saved sourceException
	if err = json.Unmarshal(raw, &saved); err != nil || !reflect.DeepEqual(saved.Event, &facts[0].event) {
		t.Fatal("missing source canonical fact changed", err)
	}
	q, err := m.Query(context.Background(), snapshot.TrajectorySelector{Text: "useful"})
	if err != nil || len(q.Sessions) != 0 {
		t.Fatal("missing source became authorized content", q, err)
	}
}

func gappedSourceMigrationFixture(t *testing.T) (*Service, *sourceState, []sourceFact, string) {
	t.Helper()
	var text strings.Builder
	for n := 0; n < 140; n++ {
		text.WriteString(request(fmt.Sprint(n) + strings.Repeat(" searchable history", 120)))
	}
	s, st := replayFixture(t, "codex", CodexDecoder{}, text.String())
	facts := canonicalFixtureFacts(t, s, st)
	path := filepath.Join(t.TempDir(), "trajectory.sqlite")
	f, err := openFactStore(path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.write(context.Background(), func(w *factWriter) error {
		row, err := w.source(st.ID, st.Agent, st.checkpoint)
		if err != nil {
			return err
		}
		if _, err = w.tx.Exec("UPDATE sources SET rowid=7 WHERE rowid=?", row); err != nil {
			return err
		}
		for _, fact := range facts {
			if _, err = w.event(7, fact.event, true); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.db.Close()
	return s, st, facts, path
}

func finishSourceFixtureMigration(t *testing.T, m *Service) {
	t.Helper()
	for n := 0; n < 300; n++ {
		err := m.openIndex()
		if err == nil {
			return
		}
		if !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
	}
	t.Fatal("bounded source migration did not finish")
}

func verifyStoredFixtureFacts(t *testing.T, m *Service, st *sourceState, facts []sourceFact) {
	t.Helper()
	var count, ranges, missing int
	if err := m.store.db.QueryRow("SELECT count(*) FROM exceptions WHERE source=7").Scan(&count); err != nil || count != len(facts) {
		t.Fatal("stored canonical cardinality", count, err)
	}
	if err := m.store.db.QueryRow("SELECT count(*) FROM ranges WHERE source=7").Scan(&ranges); err != nil || ranges != 0 {
		t.Fatal("partial replay ranges survived quarantine", ranges, err)
	}
	if err := m.store.db.QueryRow("SELECT missing FROM sources WHERE rowid=7").Scan(&missing); err != nil || missing != 1 {
		t.Fatal("original row was not quarantined", missing, err)
	}
	for _, fact := range facts {
		var body []byte
		if err := m.store.db.QueryRow("SELECT body FROM exceptions WHERE source=7 AND offset=? AND block=?", fact.offset, fact.block).Scan(&body); err != nil {
			t.Fatal(err)
		}
		raw, err := decodeSourceValue(body, maxSourceRangeLogicalBytes)
		var saved sourceException
		if err != nil || json.Unmarshal(raw, &saved) != nil || !reflect.DeepEqual(saved.Event, &fact.event) {
			t.Fatal("stored complete canonical fact changed", err)
		}
	}
	one, ok, err := m.store.checkpoint(context.Background(), st.ID)
	all, e := m.store.checkpoints(context.Background())
	if err != nil || e != nil || !ok || !one.Missing || !all[st.ID].Missing {
		t.Fatal("single and bulk quarantine evidence disagree", err, e)
	}
}

func TestTrajectorySourceMigrationPartialReplayThenWithdrawal(t *testing.T) {
	fixture, st, facts, path := gappedSourceMigrationFixture(t)
	m := NewPersistent(fixture.provider, path)
	for n := 0; n < 100; n++ {
		err := m.migrateSourceStore(context.Background())
		if !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
		state, _, err := sourceMigrationState(context.Background(), m.sourceMigration.shadow)
		if err != nil {
			t.Fatal(err)
		}
		if state.Source == 7 && state.Anchor.Offset > 0 && state.Anchor.Offset < st.checkpoint.Offset {
			break
		}
		if n == 99 {
			t.Fatal("did not stop within source")
		}
	}
	m.Close()
	m = NewPersistent(func(context.Context) SourceSet {
		return SourceSet{CatalogComplete: true, Coverage: coverage("withdrawn")}
	}, path)
	defer m.Close()
	finishSourceFixtureMigration(t, m)
	verifyStoredFixtureFacts(t, m, st, facts)
	q, err := m.Query(context.Background(), snapshot.TrajectorySelector{Text: "searchable"})
	if err != nil || len(q.Sessions) != 0 {
		t.Fatal("withdrawn stored evidence became queryable", err)
	}
	// Restoring the same file must create a new authorized generation; the
	// quarantined generation and its complete facts remain independently retained.
	m.provider = fixture.provider
	states, _ := m.collect(context.Background())
	if len(states) != 1 || states[0].Generation == st.Generation {
		t.Fatal("same-file reauthorization reused quarantined generation")
	}
	var retained int
	if err = m.store.db.QueryRow("SELECT count(*) FROM exceptions WHERE source=7").Scan(&retained); err != nil || retained != len(facts) {
		t.Fatal("reauthorization destroyed original canonical evidence", err)
	}
}

func TestTrajectorySourceMigrationStoredBatchCrashBeforeProgress(t *testing.T) {
	_, st, facts, path := gappedSourceMigrationFixture(t)
	provider := func(context.Context) SourceSet {
		return SourceSet{CatalogComplete: true, Coverage: coverage("missing")}
	}
	m := NewPersistent(provider, path)
	for n := 0; n < 100; n++ {
		err := m.migrateSourceStore(context.Background())
		if !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
		state, _, err := sourceMigrationState(context.Background(), m.sourceMigration.shadow)
		if err != nil {
			t.Fatal(err)
		}
		if state.Source == 7 && state.FactOffset >= 0 {
			// Reproduce a crash after durable facts but before durable progress.
			state.FactOffset, state.FactBlock, state.Records = -1, -1, 0
			if err = saveSourceMigration(context.Background(), m.sourceMigration.shadow, state); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	m.Close()
	m = NewPersistent(provider, path)
	defer m.Close()
	finishSourceFixtureMigration(t, m)
	verifyStoredFixtureFacts(t, m, st, facts)
}

func TestTrajectorySourceMigrationLastRangeThenSourceRewrite(t *testing.T) {
	fixture, st, facts, path := gappedSourceMigrationFixture(t)
	m := NewPersistent(fixture.provider, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.migrateSourceStore(ctx); !errors.Is(err, errStorageMigration) {
		t.Fatal("initial migration did not preserve the original", err)
	}
	lastRange := false
	capacity := m.sourceMigration.shadow.checkCapacity
	m.sourceMigration.shadow.checkCapacity = func(path string, additional uint64) error {
		state, _, err := sourceMigrationState(context.Background(), m.sourceMigration.shadow)
		if err != nil {
			return err
		}
		if state.Phase == "sources" && state.Source == 7 && state.Anchor.Offset == st.checkpoint.Offset {
			lastRange = true
			cancel()
			return ctx.Err()
		}
		return capacity(path, additional)
	}
	for n := 0; n < 100; n++ {
		err := m.migrateSourceStore(ctx)
		if lastRange {
			if !errors.Is(err, context.Canceled) {
				t.Fatal("last-range cancellation lost its cause", err)
			}
			break
		}
		if !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
	}
	if !lastRange {
		t.Fatal("did not hold last range before completion")
	}
	m.Close()
	if err := os.WriteFile(st.Path, []byte(request("replacement current evidence")), 0600); err != nil {
		t.Fatal(err)
	}
	m = NewPersistent(fixture.provider, path)
	defer m.Close()
	finishSourceFixtureMigration(t, m)
	verifyStoredFixtureFacts(t, m, st, facts)
}

func TestTrajectorySourceMigrationCurrentOwnerRecoversMissingSeal(t *testing.T) {
	fixture, st, facts, path := gappedSourceMigrationFixture(t)
	m := NewPersistent(fixture.provider, path)
	defer m.Close()
	var state sourceMigration
	for n := 0; n < 200; n++ {
		err := m.migrateSourceStore(context.Background())
		if !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
		state, _, err = sourceMigrationState(context.Background(), m.sourceMigration.shadow)
		if err != nil {
			t.Fatal(err)
		}
		if state.Phase == "ready" {
			break
		}
	}
	if state.Phase != "ready" {
		t.Fatal("missing ready state")
	}
	if err := os.Remove(path + ".source-ready.json"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writeSourceCutoverSeal(ctx, path, m.sourceMigration, state); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled seal did not cancel", err)
	}
	// Continue the exact same owner, not a replacement Service.
	finishSourceFixtureMigration(t, m)
	got, err := m.store.event(context.Background(), st, facts[0].offset, facts[0].block)
	if err != nil || !reflect.DeepEqual(got, facts[0].event) {
		t.Fatal("cancelled seal recovery changed canonical evidence", err)
	}
}

func TestTrajectorySourceMigrationTargetCountIgnoresProgressClaim(t *testing.T) {
	_, _, _, path := gappedSourceMigrationFixture(t)
	m := NewPersistent(func(context.Context) SourceSet {
		return SourceSet{CatalogComplete: true, Coverage: coverage("missing")}
	}, path)
	defer m.Close()
	var state sourceMigration
	for n := 0; n < 200; n++ {
		err := m.migrateSourceStore(context.Background())
		if !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
		state, _, err = sourceMigrationState(context.Background(), m.sourceMigration.shadow)
		if err != nil {
			t.Fatal(err)
		}
		if state.Phase == "ready" {
			break
		}
	}
	if state.Phase != "ready" {
		t.Fatal("missing ready state")
	}
	if _, err := m.sourceMigration.shadow.db.Exec("DELETE FROM exceptions WHERE rowid=(SELECT rowid FROM exceptions LIMIT 1)"); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceMigrationCounts(context.Background(), m.sourceMigration, state); err == nil {
		t.Fatal("progress claimed a target fact which no longer exists")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("failed target validation removed original", err)
	}
}

func TestTrajectorySourceMigrationOpaqueByteBudget(t *testing.T) {
	old, err := openFactStore(filepath.Join(t.TempDir(), "input.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer old.db.Close()
	value := strings.Repeat("opaque", 180000)
	if _, err = old.db.Exec("INSERT INTO meta VALUES('opaque',?)", value); err != nil {
		t.Fatal(err)
	}
	count, additional, err := sourceMigrationBatch(context.Background(), old.db, "SELECT length(CAST(key AS BLOB))+length(CAST(value AS BLOB))+128 FROM meta WHERE key='opaque'")
	if err != nil || count != 1 || additional < 2*uint64(len(value))+256*1024 {
		t.Fatal("opaque control bytes were omitted from capacity budget", count, additional, err)
	}
	path := metadataPath([][]byte{[]byte("opaque")})
	if _, err = old.db.Exec("INSERT INTO metadata VALUES(?,'0',?,0)", path, []byte(value)); err != nil {
		t.Fatal(err)
	}
	count, additional, err = sourceMigrationBatch(context.Background(), old.db, "SELECT length(path)+length(CAST(sequence AS BLOB))+COALESCE(length(body),0)+128 FROM metadata")
	if err != nil || count != 1 || additional < 2*uint64(len(value))+256*1024 {
		t.Fatal("opaque metadata bytes were omitted from capacity budget", count, additional, err)
	}
	large := strings.Repeat("x", 5*1024*1024)
	for _, key := range []string{"large-a", "large-b"} {
		if _, err = old.db.Exec("INSERT INTO meta VALUES(?,?)", key, large); err != nil {
			t.Fatal(err)
		}
	}
	count, _, err = sourceMigrationBatch(context.Background(), old.db, "SELECT length(CAST(key AS BLOB))+length(CAST(value AS BLOB))+128 FROM meta WHERE key LIKE 'large-%' ORDER BY key")
	if err != nil || count != 1 {
		t.Fatal("opaque batch exceeded its logical byte bound", count, err)
	}
}

func TestTrajectorySourceMigrationReadyRestartSealAndCutover(t *testing.T) {
	for _, mode := range []string{"resume", "corrupt", "rename-boundary"} {
		t.Run(mode, func(t *testing.T) {
			fixture, st, facts, path := gappedSourceMigrationFixture(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			m := NewPersistent(fixture.provider, path)
			ready := false
			for n := 0; n < 200; n++ {
				err = m.migrateSourceStore(context.Background())
				if !errors.Is(err, errStorageMigration) {
					t.Fatal(err)
				}
				state, _, e := sourceMigrationState(context.Background(), m.sourceMigration.shadow)
				if e != nil {
					t.Fatal(e)
				}
				if state.Phase == "ready" {
					ready = true
					break
				}
			}
			if !ready {
				t.Fatal("did not reach sealed ready checkpoint")
			}
			m.Close()
			if mode == "corrupt" {
				f, e := openSourceStore(context.Background(), path+".source-migrating")
				if e != nil {
					t.Fatal(e)
				}
				_, e = f.db.Exec("UPDATE metadata SET body=?", []byte("post-verification corruption"))
				f.db.Close()
				if e != nil {
					t.Fatal(e)
				}
			} else if mode == "rename-boundary" {
				if err = os.Rename(path, path+".source-legacy"); err != nil {
					t.Fatal(err)
				}
			}
			m = NewPersistent(fixture.provider, path)
			defer m.Close()
			if mode == "corrupt" {
				if err = m.openIndex(); err == nil || errors.Is(err, errStorageMigration) {
					t.Fatal("corrupt sealed target accepted", err)
				}
				after, e := os.ReadFile(path)
				if e != nil || !bytes.Equal(before, after) {
					t.Fatal("rejected target modified original", e)
				}
				return
			}
			finishSourceFixtureMigration(t, m)
			got, err := m.store.event(context.Background(), st, facts[0].offset, facts[0].block)
			if err != nil || !reflect.DeepEqual(got, facts[0].event) {
				t.Fatal("sealed restart changed canonical fact", err)
			}
		})
	}
}
