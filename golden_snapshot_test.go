package main

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Golden snapshot fixture for the package split (M01_S02 KR1).
//
// The point is narrow and load-bearing: moving ~19k lines of one flat package
// into bounded packages must not change a single byte of what /api/snapshot
// serves. Unit tests assert what each function was written to do, so they
// agree with a refactor that quietly drops a field. A byte comparison does
// not.
//
// Timestamps and durations are normalized rather than frozen. snapshot.Snapshot reads
// time.Now() in several places and threads real elapsed time into scan costs;
// injecting a clock everywhere would be a larger change than the refactor it
// guards. Normalizing keeps the comparison byte-exact over everything that is
// not a clock reading, which is where a refactor actually goes wrong.

const goldenSnapshotPath = "testdata/golden_snapshot.json"

var goldenVolatileFields = []*regexp.Regexp{
	// RFC3339 instants, with or without fractional seconds.
	regexp.MustCompile(`"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))"`),
	// Any field whose name ends in _ms / _seconds / _at plus elapsed counters.
	regexp.MustCompile(`"(?:[a-z_]*_(?:ms|seconds|at)|elapsed_ms|uptime_seconds)":\s*-?\d+(?:\.\d+)?`),
}

// goldenHostNumber blanks the numeric readings inside system_resources. Those
// are live measurements of this machine -- memory, disk, load, network
// counters -- so they differ between two runs seconds apart and would make the
// gate fail for reasons that have nothing to do with a refactor. Only the
// values are blanked: every key is still compared, so a field the refactor
// drops still fails the gate.
var goldenHostNumber = regexp.MustCompile(`("(?:cpu_percent|load_average_\d+|memory_[a-z_]+|disk_[a-z_]+|network_[a-z_]+)":\s*)-?\d+(?:\.\d+)?`)

// goldenTempRoot matches t.TempDir(). Host prefix and per-run name are
// replaced so the fixture is not a machine path; only the leaf shape stays.
var goldenTempRoot = regexp.MustCompile(`(?:/var/folders/[^/]+/[^/]+/T|/tmp)/TestSnapshotMatchesGolden\d+/`)

func normalizeGoldenSnapshot(raw []byte) []byte {
	out := raw
	out = goldenVolatileFields[0].ReplaceAll(out, []byte(`"<instant>"`))
	out = goldenVolatileFields[1].ReplaceAllFunc(out, func(match []byte) []byte {
		name := match[:bytes.LastIndexByte(match, ':')]
		return append(append([]byte{}, name...), []byte(`: "<duration>"`)...)
	})
	out = goldenTempRoot.ReplaceAll(out, []byte("/<tmp>/"))
	out = goldenHostNumber.ReplaceAll(out, []byte(`${1}"<host>"`))
	return out
}

// hermeticGoldenObserver builds an Observer over an empty temp root so the
// fixture describes the code's shape rather than this machine's transcripts.
func hermeticGoldenObserver(t *testing.T) *Observer {
	t.Helper()
	root := t.TempDir()
	cfg := Config{
		IdleGap:       90 * time.Second,
		MinInterval:   15 * time.Second,
		Lookback:      time.Hour,
		ClaudeRoots:   []string{filepath.Join(root, "claude")},
		CodexRoots:    []string{filepath.Join(root, "codex")},
		TraeRoots:     []string{filepath.Join(root, "trae")},
		GrokRoots:     []string{filepath.Join(root, "grok")},
		GeminiRoots:   []string{filepath.Join(root, "gemini")},
		OpenCodeRoots: []string{filepath.Join(root, "opencode")},
		HermesRoots:   []string{filepath.Join(root, "hermes")},
		OpenClawRoots: []string{filepath.Join(root, "openclaw")},
		PiRoots:       []string{filepath.Join(root, "pi")},
	}
	original := discoverLiveProcessesFunc
	discoverLiveProcessesFunc = func(context.Context, *codingAgentRegistry) ([]snapshot.LiveProcess, []string) {
		// A fixed roster, so the fixture covers the session/project/role
		// assembly rather than only the empty case.
		return []snapshot.LiveProcess{
			{PID: 4101, Tool: "claude", Command: "claude --resume abc123"},
			{PID: 4102, Tool: "codex", Command: "codex exec"},
			{PID: 4103, Tool: "gemini", Command: "gemini --prompt review"},
		}, nil
	}
	t.Cleanup(func() { discoverLiveProcessesFunc = original })
	return newObserver(cfg)
}

// TestSnapshotMatchesGolden is the byte-level gate for the package split.
// Regenerate deliberately with UPDATE_GOLDEN_SNAPSHOT=1 and read the diff.
func TestSnapshotMatchesGolden(t *testing.T) {
	observer := hermeticGoldenObserver(t)
	got := observer.Snapshot(context.Background())

	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	normalized := append(normalizeGoldenSnapshot(encoded), '\n')

	want, err := os.ReadFile(goldenSnapshotPath)
	if err != nil || os.Getenv("UPDATE_GOLDEN_SNAPSHOT") != "" {
		if os.Getenv("UPDATE_GOLDEN_SNAPSHOT") == "" {
			t.Fatalf("read golden snapshot: %v\nGenerate it with:\n  UPDATE_GOLDEN_SNAPSHOT=1 go test . -run TestSnapshotMatchesGolden", err)
		}
		if err := os.MkdirAll(filepath.Dir(goldenSnapshotPath), 0o755); err != nil {
			t.Fatalf("create testdata dir: %v", err)
		}
		if err := os.WriteFile(goldenSnapshotPath, normalized, 0o644); err != nil {
			t.Fatalf("write golden snapshot: %v", err)
		}
		t.Fatalf("golden snapshot regenerated; re-run without UPDATE_GOLDEN_SNAPSHOT")
	}
	if string(normalized) != string(want) {
		t.Fatalf("snapshot shape changed.\nIf this is deliberate, regenerate with:\n  UPDATE_GOLDEN_SNAPSHOT=1 go test . -run TestSnapshotMatchesGolden\n\ngot:\n%s", normalized)
	}
}

// TestGoldenNormalizerKeepsRealValues pins the normalizer itself. A normalizer
// that erased too much would make the golden gate pass through any refactor,
// which is the failure mode that matters here: a guard that cannot fail.
func TestGoldenNormalizerKeepsRealValues(t *testing.T) {
	raw := []byte(`{"generated_at":"2026-09-20T17:47:55.249651+08:00","elapsed_ms":512,"visited_entries":28223,"pid_concurrency":3,"state":"unavailable"}`)
	got := string(normalizeGoldenSnapshot(raw))
	for _, keep := range []string{`"visited_entries":28223`, `"pid_concurrency":3`, `"state":"unavailable"`} {
		if !strings.Contains(got, keep) {
			t.Fatalf("normalizer erased a real value %q, got %s", keep, got)
		}
	}
	for _, gone := range []string{"2026-09-20T17:47:55", "512"} {
		if strings.Contains(got, gone) {
			t.Fatalf("normalizer left volatile value %q in %s", gone, got)
		}
	}
}
