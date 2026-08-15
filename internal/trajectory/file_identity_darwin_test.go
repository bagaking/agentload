package trajectory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

type identityFileInfo struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (i identityFileInfo) Sys() any { return &i.stat }

func TestTrajectoryPersistentIdentitySurvivesDeviceChangeRejectsVolumeChange(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("local research"))
	f, err := os.Open(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	before, err := persistentFileIdentity(f, info)
	if err != nil {
		t.Fatal(err)
	}
	changed := identityFileInfo{info, *info.Sys().(*syscall.Stat_t)}
	changed.stat.Dev++
	after, err := persistentFileIdentity(f, changed)
	if err != nil || before != after || !strings.HasPrefix(before, "volume-v1:") {
		t.Fatal("remount changed persistent identity", before, after, err)
	}
	changed.stat.Ino++
	replaced, err := persistentFileIdentity(f, changed)
	if err != nil || replaced == before {
		t.Fatal("inode replacement retained identity", err)
	}
	copy := *st
	parts := strings.Split(before, ":")
	parts[1] = strings.Repeat("0", 32)
	copy.checkpoint.Identity = strings.Join(parts, ":")
	// A sealed negative candidate cannot reuse another volume's identity,
	// even if path, inode, mtime, size and small anchors are identical.
	err = s.store.walkSourceCandidates(context.Background(), &copy, func(*sourceFilter) bool { return false }, nil, func(sourceFact) error { t.Fatal("false hit"); return nil })
	if !errors.Is(err, ErrStale) {
		t.Fatal("different volume yielded a successful empty search", err)
	}
}

func saveIdentityFixture(t *testing.T, f *sourceStore, st *sourceState, cp sourceCheckpoint) {
	t.Helper()
	raw, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	body, err := encodeSourceValue(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE sources SET checkpoint=? WHERE id=? AND generation=?", body, st.ID, st.Generation); err != nil {
		t.Fatal(err)
	}
}

func TestTrajectoryLegacyIdentityUpgradePinsBoundedProgressAndPublicIDs(t *testing.T) {
	var body strings.Builder
	for n := 0; n < 75; n++ {
		body.WriteString(request(fmt.Sprintf("record %d ", n) + strings.Repeat("trajectory ", 12500)))
	}
	s, st := replayFixture(t, "codex", CodexDecoder{}, body.String())
	info, err := os.Stat(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	cp := st.checkpoint
	native := info.Sys().(*syscall.Stat_t)
	cp.Identity = fmt.Sprintf("%d:%d", native.Dev+1, native.Ino)
	saveIdentityFixture(t, s.store, st, cp)
	var originalFrame []byte
	if err = s.store.db.QueryRow("SELECT checkpoint FROM sources").Scan(&originalFrame); err != nil {
		t.Fatal(err)
	}
	originalJSON, err := decodeSourceValue(originalFrame, 2*maxRecordBytes)
	if err != nil {
		t.Fatal(err)
	}
	var unknown map[string]json.RawMessage
	if err = json.Unmarshal(originalJSON, &unknown); err != nil {
		t.Fatal(err)
	}
	unknown["future_checkpoint_metadata"] = json.RawMessage(`{"nested":[1,"keep exact bytes"]}`)
	originalJSON, err = json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	originalFrame, err = encodeSourceValue(originalJSON)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.db.Exec("UPDATE sources SET checkpoint=?", originalFrame); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.db.Exec("UPDATE sources SET audit_size=?,audit_mtime=?,audit_after=?,verified_size=?,verified_mtime=?", info.Size(), info.ModTime().UnixNano(), info.Size(), info.Size(), info.ModTime().UnixNano()); err != nil {
		t.Fatal(err)
	}
	first, err := s.store.upgradeFileIdentity(context.Background(), st.Source, cp, info, true, 64*1024*1024, time.Time{})
	if !errors.Is(err, errStorageMigration) || first.Identity != cp.Identity || first.PendingIdentity != st.checkpoint.Identity {
		t.Fatal("old checkpoint was published before full proof", first, err)
	}
	var verified int64
	if err = s.store.db.QueryRow("SELECT verified_size FROM sources").Scan(&verified); err != nil || verified != -1 {
		t.Fatal("legacy negative seal survived", verified, err)
	}
	// Simulate a resumed audit binding belonging to another volume. Its
	// claimed progress must be discarded before this volume can be sealed.
	first.PendingIdentity = "volume-v1:" + strings.Repeat("0", 32) + fmt.Sprintf(":%d", native.Ino)
	saveIdentityFixture(t, s.store, st, first)
	second, err := s.store.upgradeFileIdentity(context.Background(), st.Source, first, info, true, 64*1024*1024, time.Time{})
	if !errors.Is(err, errStorageMigration) || second.Identity != cp.Identity {
		t.Fatal("cross-volume audit progress skipped old ranges", second, err)
	}
	third, err := s.store.upgradeFileIdentity(context.Background(), st.Source, second, info, true, 64*1024*1024, time.Time{})
	if err != nil || third.Identity != st.checkpoint.Identity || third.Generation != st.Generation || third.EventCount != st.checkpoint.EventCount || third.PendingIdentity != "" {
		t.Fatal("lossless identity upgrade failed", third, err)
	}
	st.checkpoint = third
	var preservedFrame []byte
	if err = s.store.db.QueryRow("SELECT body FROM metadata WHERE path=?", metadataPath([][]byte{[]byte("source-identity-v1"), []byte(st.ID), []byte(st.Generation)})).Scan(&preservedFrame); err != nil || !bytes.Equal(originalFrame, preservedFrame) {
		t.Fatal("unknown checkpoint bytes lost in online upgrade", err)
	}
	events, _, err := s.store.eventsForTest(context.Background(), st)
	if err != nil || len(events) != 75 {
		t.Fatal("canonical facts changed", len(events), err)
	}
}

// Use the actual replay owner to check complete public facts after upgrade.
func (f *sourceStore) eventsForTest(ctx context.Context, st *sourceState) ([]sourceFact, int, error) {
	var facts []sourceFact
	err := f.walkSourceCandidates(ctx, st, nil, nil, func(x sourceFact) error { facts = append(facts, x); return nil })
	return facts, len(facts), err
}

func TestTrajectoryLegacyInputRebindRequiresPriorFullDigestAndResumesSameShadow(t *testing.T) {
	fixture, st, facts, path := gappedSourceMigrationFixture(t)
	original, err := openFactStore(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	cp := st.checkpoint
	native := info.Sys().(*syscall.Stat_t)
	cp.Identity = fmt.Sprintf("%d:%d", native.Dev+1, native.Ino)
	if err = original.write(context.Background(), func(w *factWriter) error { _, e := w.source(st.ID, st.Agent, cp); return e }); err != nil {
		t.Fatal(err)
	}
	original.db.Close()
	expected, _, err := sourceFileSeal(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	m := NewPersistent(fixture.provider, path)
	if err = m.migrateSourceStore(context.Background()); !errors.Is(err, errStorageMigration) {
		t.Fatal(err)
	}
	state, _, err := sourceMigrationState(context.Background(), m.sourceMigration.shadow)
	if err != nil {
		t.Fatal(err)
	}
	dbinfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dbstat := dbinfo.Sys().(*syscall.Stat_t)
	state.Input = fmt.Sprintf("%d:%d:%d:%d", dbstat.Dev+1, dbstat.Ino, dbinfo.Size(), dbinfo.ModTime().UnixNano())
	state.InputIdentity, state.InputSHA256 = "", ""
	state.IdentitiesComplete = false
	if err = saveSourceMigration(context.Background(), m.sourceMigration.shadow, state); err != nil {
		t.Fatal(err)
	}
	m.Close()
	for _, value := range []string{"", strings.Repeat("0", 64)} {
		bad := NewPersistent(fixture.provider, path)
		bad.expectedInputSHA256 = value
		if err = bad.migrateSourceStore(context.Background()); err == nil || errors.Is(err, errStorageMigration) {
			t.Fatal("unproved input rebound", value, err)
		}
		bad.Close()
		hash, _, e := sourceFileSeal(context.Background(), path)
		if e != nil || hash != expected {
			t.Fatal("refusal changed original", e)
		}
	}
	first := NewPersistent(fixture.provider, path)
	first.expectedInputSHA256 = expected
	for n := 0; n < 100; n++ {
		e := first.migrateSourceStore(context.Background())
		if !errors.Is(e, errStorageMigration) {
			t.Fatal(e)
		}
		p, _, e := sourceMigrationState(context.Background(), first.sourceMigration.shadow)
		if e != nil {
			t.Fatal(e)
		}
		if p.Anchor.Offset > 0 {
			break
		}
		if n == 99 {
			t.Fatal("no committed source batch")
		}
	}
	shadowInfo, err := os.Stat(path + ".source-migrating")
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	second := NewPersistent(fixture.provider, path)
	defer second.Close()
	finishSourceFixtureMigration(t, second)
	targetInfo, err := os.Stat(path)
	if err != nil || !os.SameFile(shadowInfo, targetInfo) {
		t.Fatal("resume manufactured another shadow", err)
	}
	final, _, err := sourceMigrationState(context.Background(), second.store)
	if err != nil || final.Input != state.Input || final.InputSHA256 != expected || final.Records != int64(len(facts)) {
		t.Fatal("input namespace or count changed", final, err)
	}
	bound, has, err := second.store.checkpoint(context.Background(), st.ID)
	if err != nil || !has || bound.Generation != st.Generation || bound.Identity != st.checkpoint.Identity {
		t.Fatal("public generation changed", bound, err)
	}
	var saved []byte
	if err = second.store.db.QueryRow("SELECT body FROM metadata WHERE path=?", migrationRecoveryPath(state.Input, "checkpoint", "7")).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	raw, err := decodeStored(saved)
	var preserved sourceCheckpoint
	if err != nil || json.Unmarshal(raw, &preserved) != nil || !reflect.DeepEqual(cp, preserved) {
		t.Fatal("original checkpoint bytes lost", err)
	}
	st.checkpoint = bound
	got, _, err := second.store.eventsForTest(context.Background(), st)
	if err != nil || !reflect.DeepEqual(facts, got) {
		t.Fatal("full canonical facts changed after remount", err)
	}
}

func TestTrajectoryLegacyReadyIdentityUpgradeCanRestartBeforeNewSeal(t *testing.T) {
	m, states, _, path, _ := completedMigrationFixture(t)
	state, _, err := sourceMigrationState(context.Background(), m.sourceMigration.shadow)
	if err != nil {
		t.Fatal(err)
	}
	oldCount, oldNamespace := state.Records, state.Input
	for _, st := range states {
		cp := st.checkpoint
		cp.Identity = statIdentity(st.Info)
		saveIdentityFixture(t, m.sourceMigration.shadow, st, cp)
	}
	state.InputIdentity, state.InputSHA256 = "", ""
	state.IdentityAfter, state.IdentitiesComplete = 0, false
	if err = saveSourceMigration(context.Background(), m.sourceMigration.shadow, state); err != nil {
		t.Fatal(err)
	}
	// A real old ready seal pins both its physical target and progress. The
	// first upgraded write intentionally invalidates that physical hash.
	if err = writeSourceCutoverSeal(context.Background(), path, m.sourceMigration, state); err != nil {
		t.Fatal(err)
	}
	m.Close()
	first := NewPersistent(m.provider, path)
	if err = first.migrateSourceStore(context.Background()); !errors.Is(err, errStorageMigration) {
		t.Fatal(err)
	}
	unfinished, _, err := sourceMigrationState(context.Background(), first.sourceMigration.shadow)
	if err != nil || unfinished.Phase == "ready" || unfinished.Records != oldCount || unfinished.Input != oldNamespace || unfinished.InputIdentity == "" {
		t.Fatal("first upgraded write did not pin unfinished recovery", unfinished, err)
	}
	first.Close()
	second := NewPersistent(m.provider, path)
	defer second.Close()
	finishSourceFixtureMigration(t, second)
	final, _, err := sourceMigrationState(context.Background(), second.store)
	if err != nil || final.Records != oldCount || final.Input != oldNamespace {
		t.Fatal("ready upgrade lost facts or namespace", final, err)
	}
}

func TestTrajectoryLegacyReadyWithoutSealRechecksOpaqueRecovery(t *testing.T) {
	m, _, _, path, _ := completedMigrationFixture(t)
	state, _, err := sourceMigrationState(context.Background(), m.sourceMigration.shadow)
	if err != nil {
		t.Fatal(err)
	}
	state.InputIdentity, state.InputSHA256 = "", ""
	state.IdentityAfter, state.IdentitiesComplete = 0, false
	if err = saveSourceMigration(context.Background(), m.sourceMigration.shadow, state); err != nil {
		t.Fatal(err)
	}
	result, err := m.sourceMigration.shadow.db.Exec("UPDATE metadata SET body=? WHERE path=?", []byte("changed opaque revision"), migrationRecoveryPath(state.Input, "meta", "revision"))
	if err != nil {
		t.Fatal(err)
	}
	if n, e := result.RowsAffected(); e != nil || n != 1 {
		t.Fatal("fixture did not alter original recovery evidence", n, e)
	}
	if err = os.Remove(path + ".source-ready.json"); err != nil {
		t.Fatal(err)
	}
	m.Close()
	restarted := NewPersistent(m.provider, path)
	defer restarted.Close()
	for n := 0; n < 30; n++ {
		err = restarted.migrateSourceStore(context.Background())
		if !errors.Is(err, errStorageMigration) {
			if err == nil || !strings.Contains(err.Error(), "metadata") {
				t.Fatal("unsealed opaque metadata was not rejected by its owning evidence check", err)
			}
			if _, e := os.Stat(path); e != nil {
				t.Fatal("original was retired", e)
			}
			if _, e := os.Stat(path + ".source-ready.json"); !os.IsNotExist(e) {
				t.Fatal("bad recovery was newly sealed", e)
			}
			return
		}
	}
	t.Fatal("unsealed opaque metadata was not rechecked")
}

func TestTrajectoryOnlineMalformedIdentityPreservedAndRealReplacementReindexed(t *testing.T) {
	for _, identity := range []string{"corrupt", "volume-v1:bad:7", "volume-v1:" + strings.Repeat("a", 32) + ":bad"} {
		t.Run(identity, func(t *testing.T) {
			s, st := replayFixture(t, "codex", CodexDecoder{}, request("preserve original research"))
			cp := st.checkpoint
			cp.Identity = identity
			saveIdentityFixture(t, s.store, st, cp)
			var before []byte
			if err := s.store.db.QueryRow("SELECT checkpoint FROM sources").Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.indexSource(context.Background(), st.Source, 0, 1024*1024, time.Time{}); err == nil {
				t.Fatal("malformed identity rebuilt an apparently new source")
			}
			var after []byte
			var rows int
			if err := s.store.db.QueryRow("SELECT checkpoint FROM sources").Scan(&after); err != nil {
				t.Fatal(err)
			}
			if err := s.store.db.QueryRow("SELECT count(*) FROM sources").Scan(&rows); err != nil || rows != 1 || !bytes.Equal(before, after) {
				t.Fatal("damaged checkpoint or generation was replaced", rows, err)
			}
		})
	}
	t.Run("real inode replacement", func(t *testing.T) {
		s, st := replayFixture(t, "codex", CodexDecoder{}, request("same text on a replacement file"))
		cp := st.checkpoint
		cp.Identity = statIdentity(st.Info)
		saveIdentityFixture(t, s.store, st, cp)
		body, err := os.ReadFile(st.Path)
		if err != nil {
			t.Fatal(err)
		}
		replacement := st.Path + ".replacement"
		if err = os.WriteFile(replacement, body, 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.Chtimes(replacement, st.Info.ModTime(), st.Info.ModTime()); err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(replacement, st.Path); err != nil {
			t.Fatal(err)
		}
		current, err := s.store.indexSource(context.Background(), st.Source, 0, 1024*1024, time.Time{})
		if err != nil || current.Generation == st.Generation || current.checkpoint.EventCount != cp.EventCount {
			t.Fatal("real replacement was mistaken for a remount", err)
		}
		var retained int
		if err = s.store.db.QueryRow("SELECT count(*) FROM sources WHERE id=? AND generation=? AND active=0", st.ID, st.Generation).Scan(&retained); err != nil || retained != 1 {
			t.Fatal("old generation lost", err)
		}
	})
}
