package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestTrajectorySourceMigrationChangeAtWriteAndReadback(t *testing.T) {
	for _, boundary := range []string{"write", "readback", "cancel", "corrupt"} {
		t.Run(boundary, func(t *testing.T) {
			fixture, st, facts, path := gappedSourceMigrationFixture(t)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rawBefore, err := os.ReadFile(st.Path)
			if err != nil {
				t.Fatal(err)
			}
			m := NewPersistent(fixture.provider, path)
			defer m.Close()
			for n := 0; ; n++ {
				if n > 100 {
					t.Fatal("did not reach source copy")
				}
				if err = m.migrateSourceStore(context.Background()); !errors.Is(err, errStorageMigration) {
					t.Fatal(err)
				}
				state, _, e := sourceMigrationState(context.Background(), m.sourceMigration.shadow)
				if e != nil {
					t.Fatal(e)
				}
				if state.Phase == "sources" {
					break
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fired := false
			change := func() {
				if fired {
					return
				}
				fired = true
				if boundary == "cancel" {
					cancel()
					return
				}
				if boundary == "corrupt" {
					var body []byte
					if e := m.sourceMigration.shadow.db.QueryRow("SELECT body FROM ranges WHERE source=7 AND start=0").Scan(&body); e != nil {
						t.Fatal(e)
					}
					raw, e := decodeSourceValue(body, 3*maxRecordBytes)
					var value sourceRange
					if e != nil || json.Unmarshal(raw, &value) != nil {
						t.Fatal("invalid fixture range", e)
					}
					value.Facts++
					raw, _ = json.Marshal(value)
					body, e = encodeSourceValue(raw)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = m.sourceMigration.shadow.db.Exec("UPDATE ranges SET body=? WHERE source=7 AND start=0", body); e != nil {
						t.Fatal(e)
					}
					return
				}
				f, e := os.OpenFile(st.Path, os.O_APPEND|os.O_WRONLY, 0600)
				if e != nil {
					t.Fatal(e)
				}
				_, e = f.WriteString(request("appended during physical migration"))
				closeErr := f.Close()
				if e != nil || closeErr != nil {
					t.Fatal(e, closeErr)
				}
			}
			shadow := m.sourceMigration.shadow
			capacity := shadow.checkCapacity
			if boundary == "readback" || boundary == "corrupt" {
				d := &migrationReadbackDecoder{before: func() {
					var count int
					if e := shadow.db.QueryRow("SELECT count(*) FROM ranges WHERE source=7 AND start=0").Scan(&count); e != nil {
						t.Fatal(e)
					}
					if count == 1 {
						change()
					}
				}}
				src := st.Source
				src.Decoder = d
				m.provider = func(context.Context) SourceSet {
					return SourceSet{Sources: []Source{src}, CatalogComplete: true, Coverage: coverage("migration boundary fixture")}
				}
			} else {
				shadow.checkCapacity = func(p string, additional uint64) error {
					// The source-checkpoint write is smaller; fire only after the
					// range has been rebuilt, at its durable transaction boundary.
					if additional >= 4*1024*1024+512*1024 {
						change()
					}
					return capacity(p, additional)
				}
			}
			err = m.migrateSourceStore(ctx)
			if boundary == "corrupt" {
				for n := 0; errors.Is(err, errStorageMigration) && n < 100; n++ {
					state, _, e := sourceMigrationState(context.Background(), shadow)
					if e != nil || state.Mode == "stored" {
						t.Fatal("damaged proof was classified as a source change", state, e)
					}
					err = m.migrateSourceStore(ctx)
				}
			}
			want := errStorageMigration
			if boundary == "cancel" {
				want = context.Canceled
			} else if boundary == "corrupt" {
				want = ErrStale
			}
			if !fired || !errors.Is(err, want) {
				t.Fatal("source boundary did not retain recoverable progress", fired, err)
			}
			state, _, err := sourceMigrationState(context.Background(), shadow)
			if err != nil || (boundary != "corrupt" && (state.Anchor.Offset != 0 || state.Records != 0)) || ((boundary == "write" || boundary == "readback") && state.Mode != "stored") || ((boundary == "cancel" || boundary == "corrupt") && state.Mode != "") {
				t.Fatal("unverified source advanced, or cancellation became quarantine", state, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatal("canonical input changed", err)
			}
			rawAfter, err := os.ReadFile(st.Path)
			if err != nil || !bytes.HasPrefix(rawAfter, rawBefore) {
				t.Fatal("source prefix changed", err)
			}
			if boundary == "corrupt" {
				var complete int
				if e := shadow.db.QueryRow("SELECT complete FROM sources WHERE rowid=7").Scan(&complete); e != nil || complete != 0 {
					t.Fatal("damaged proof published a prepared source", complete, e)
				}
				return
			}
			if err = m.Close(); err != nil {
				t.Fatal(err)
			}
			m = NewPersistent(fixture.provider, path)
			defer m.Close()
			finishSourceFixtureMigration(t, m)
			if boundary != "cancel" {
				verifyStoredFixtureFacts(t, m, st, facts)
			}
		})
	}
}

type migrationReadbackDecoder struct{ before func() }

func (d *migrationReadbackDecoder) Decode(raw []byte, ctx DecodeContext) ([]snapshot.TrajectoryEvent, error) {
	if d.before != nil {
		d.before()
	}
	return (CodexDecoder{}).Decode(raw, ctx)
}
