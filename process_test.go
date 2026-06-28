package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestParsePSLine(t *testing.T) {
	uid, pid, command, ok := parsePSLine(`  501  4242 /Applications/Codex.app/Contents/MacOS/Codex --thread-id 123e4567-e89b-12d3-a456-426614174000`)
	if !ok {
		t.Fatalf("expected ps line to parse")
	}
	if uid != 501 || pid != 4242 {
		t.Fatalf("unexpected uid/pid: %d %d", uid, pid)
	}
	if command != `/Applications/Codex.app/Contents/MacOS/Codex --thread-id 123e4567-e89b-12d3-a456-426614174000` {
		t.Fatalf("unexpected command: %q", command)
	}

	if _, _, _, ok := parsePSLine(`bad 4242 codex`); ok {
		t.Fatalf("expected invalid ps line to fail")
	}
}

func TestParseProcessTableLineIncludesPPID(t *testing.T) {
	row, ok := parseProcessTableLine(`  501  4242  101 /usr/local/bin/codex --thread-id 123e4567-e89b-12d3-a456-426614174000`)
	if !ok {
		t.Fatalf("expected process table line to parse")
	}
	if row.UID != 501 || row.PID != 4242 || row.PPID != 101 {
		t.Fatalf("unexpected row ids: %#v", row)
	}
	if row.Command != `/usr/local/bin/codex --thread-id 123e4567-e89b-12d3-a456-426614174000` {
		t.Fatalf("unexpected command: %q", row.Command)
	}
}

func TestDetectedTool(t *testing.T) {
	cases := []struct {
		command string
		want    string
	}{
		{command: `/usr/local/bin/codexL as-agent watch`, want: "codex"},
		{command: `/usr/local/bin/traex --yolo resume 019f0abc`, want: "trae"},
		{command: `/usr/local/bin/trae --yolo resume 019f0abc`, want: "trae"},
		{command: `/Applications/Codex.app/Contents/MacOS/Codex`, want: "codex"},
		{command: `Codex Computer Use.app/Contents/MacOS/Codex Computer Use`, want: "codex"},
		{command: `/Applications/Claude.app/Contents/MacOS/Claude`, want: "claude"},
		{command: `/Applications/Codex.app/Contents/MacOS/Updater.app --sparkle`, want: ""},
		{command: ``, want: ""},
	}
	for _, tc := range cases {
		if got := detectedTool(tc.command); got != tc.want {
			t.Fatalf("detectedTool(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

func TestAppBundlePathFromCommandHandlesSpaces(t *testing.T) {
	root := t.TempDir()
	bundlePath := filepath.Join(root, "Visual Studio Code.app")
	if err := os.MkdirAll(filepath.Join(bundlePath, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	command := `"` + filepath.Join(bundlePath, "Contents", "MacOS", "Electron") + `" --reuse-window`
	if got := appBundlePathFromCommand(command); got != bundlePath {
		t.Fatalf("expected %q, got %q", bundlePath, got)
	}
}

func TestAppBundlePathFromCommandIgnoresArgumentOnlyBundlePaths(t *testing.T) {
	root := t.TempDir()
	bundlePath := filepath.Join(root, "Argument Only.app")
	if err := os.MkdirAll(filepath.Join(bundlePath, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	command := `/usr/local/bin/codex --asset "` + filepath.Join(bundlePath, "Contents", "Resources", "icon.png") + `"`
	if got := appBundlePathFromCommand(command); got != "" {
		t.Fatalf("expected argument-only bundle path to be ignored, got %q", got)
	}
}

func TestInferHostAppFromParentChain(t *testing.T) {
	root := t.TempDir()
	bundlePath := filepath.Join(root, "Terminal.app")
	if err := os.MkdirAll(filepath.Join(bundlePath, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	processes := map[int]processRow{
		100: {UID: 501, PID: 100, PPID: 1, Command: filepath.Join(bundlePath, "Contents", "MacOS", "Terminal")},
		200: {UID: 501, PID: 200, PPID: 100, Command: `/usr/local/bin/codex --thread-id 123e4567-e89b-12d3-a456-426614174000`},
	}
	app := inferHostApp(processes[200], processes)
	if app == nil {
		t.Fatalf("expected host app")
	}
	if app.PID != 100 || app.Name != "Terminal" || app.BundlePath != bundlePath {
		t.Fatalf("unexpected host app: %#v", app)
	}
}

func TestInferHostAppIgnoresArgumentOnlyBundlePaths(t *testing.T) {
	root := t.TempDir()
	bundlePath := filepath.Join(root, "Argument Only.app")
	if err := os.MkdirAll(filepath.Join(bundlePath, "Contents", "Resources"), 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	processes := map[int]processRow{
		200: {
			UID:  501,
			PID:  200,
			PPID: 1,
			Command: `/usr/local/bin/codex --asset "` +
				filepath.Join(bundlePath, "Contents", "Resources", "icon.png") + `"`,
		},
	}
	if app := inferHostApp(processes[200], processes); app != nil {
		t.Fatalf("expected argument-only bundle path to be ignored, got %#v", app)
	}
}

func TestTranscriptFileFromPath(t *testing.T) {
	cases := []struct {
		path     string
		wantTool string
		wantOK   bool
	}{
		{path: filepath.Join("/tmp", "alice", ".codex", "sessions", "abc.jsonl"), wantTool: "codex", wantOK: true},
		{path: filepath.Join("/tmp", "alice", ".codex", "archived_sessions", "abc.jsonl"), wantTool: "codex", wantOK: true},
		{path: filepath.Join("/tmp", "alice", ".codex", ".codexl", "asagent", "lane-1", "events.jsonl"), wantTool: "codex", wantOK: true},
		{path: filepath.Join("/tmp", "alice", ".claude", "projects", "project-a", "trace.jsonl"), wantTool: "claude", wantOK: true},
		{path: filepath.Join("/tmp", "alice", ".trae", "cli", "sessions", "2026", "06", "28", "trace.jsonl"), wantTool: "trae", wantOK: true},
		{path: filepath.Join("/tmp", "alice", ".codex", "sessions", "abc.txt"), wantTool: "", wantOK: false},
	}
	for _, tc := range cases {
		got, ok := transcriptFileFromPath(tc.path)
		if ok != tc.wantOK {
			t.Fatalf("transcriptFileFromPath(%q) ok = %v, want %v", tc.path, ok, tc.wantOK)
		}
		if !ok {
			continue
		}
		if got.Tool != tc.wantTool {
			t.Fatalf("transcriptFileFromPath(%q) tool = %q, want %q", tc.path, got.Tool, tc.wantTool)
		}
		if got.Path != filepath.Clean(tc.path) {
			t.Fatalf("transcriptFileFromPath(%q) path = %q, want %q", tc.path, got.Path, filepath.Clean(tc.path))
		}
	}
}

func TestExtractSessionHints(t *testing.T) {
	command := `codexL as-agent exec --thread-id 123e4567-e89b-12d3-a456-426614174000 CODEX_THREAD_ID=aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee '{"sessionId":"123e4567-e89b-12d3-a456-426614174000","session_id":"ffffffff-1111-2222-3333-444444444444"}'`
	want := []string{
		"123e4567-e89b-12d3-a456-426614174000",
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"ffffffff-1111-2222-3333-444444444444",
	}
	if got := extractSessionHints(command); !slices.Equal(got, want) {
		t.Fatalf("unexpected session hints: %#v", got)
	}
}

func TestConfigRootFromPathFindsCodexRoot(t *testing.T) {
	root := t.TempDir()
	codexRoot := filepath.Join(root, "agentload", ".codex")
	lanePath := filepath.Join(codexRoot, ".codexl", "asagent", "lane-1", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(lanePath), 0o755); err != nil {
		t.Fatalf("mkdir lane path: %v", err)
	}
	if got := configRootFromPath(lanePath, ".codex"); got != codexRoot {
		t.Fatalf("expected codex root %q, got %q", codexRoot, got)
	}
	if got := configRootFromPath(filepath.Join(root, "missing", ".codex", "sessions", "a.jsonl"), ".codex"); got != "" {
		t.Fatalf("expected missing root to stay empty, got %q", got)
	}
}

func TestRootsFromLiveProcessesCollectsFileAndCommandRoots(t *testing.T) {
	root := t.TempDir()
	projectCodexRoot := filepath.Join(root, "agentload", ".codex")
	homeCodexRoot := filepath.Join(root, "alice", ".codex")
	claudeRoot := filepath.Join(root, "alice", ".claude")
	traeRoot := filepath.Join(root, "alice", ".trae", "cli")
	if err := os.MkdirAll(filepath.Join(projectCodexRoot, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir project codex: %v", err)
	}
	if err := os.MkdirAll(homeCodexRoot, 0o755); err != nil {
		t.Fatalf("mkdir home codex: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(claudeRoot, "projects", "project-a"), 0o755); err != nil {
		t.Fatalf("mkdir claude root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(traeRoot, "sessions", "2026", "06", "28"), 0o755); err != nil {
		t.Fatalf("mkdir trae root: %v", err)
	}

	codexSession := filepath.Join(projectCodexRoot, "sessions", "codex-session.jsonl")
	claudeSession := filepath.Join(claudeRoot, "projects", "project-a", "events.jsonl")
	traeSession := filepath.Join(traeRoot, "sessions", "2026", "06", "28", "trae-session.jsonl")
	processes := []LiveProcess{
		{
			PID:     1,
			Tool:    "codex",
			Command: `codexL --home ` + homeCodexRoot + ` as-agent watch`,
			SessionFiles: []TranscriptFile{
				{Tool: "codex", Path: codexSession},
			},
		},
		{
			PID:     2,
			Tool:    "claude",
			Command: `claude --config ` + claudeRoot,
			SessionFiles: []TranscriptFile{
				{Tool: "claude", Path: claudeSession},
			},
		},
		{
			PID:     3,
			Tool:    "trae",
			Command: `traex --home ` + traeRoot + ` --yolo resume 019f0abc`,
			SessionFiles: []TranscriptFile{
				{Tool: "trae", Path: traeSession},
			},
		},
	}

	claudeRoots, codexRoots, traeRoots, priority := rootsFromLiveProcesses(processes)
	if !slices.Equal(claudeRoots, []string{claudeRoot}) {
		t.Fatalf("unexpected claude roots: %#v", claudeRoots)
	}
	if !slices.Equal(codexRoots, []string{projectCodexRoot, homeCodexRoot}) {
		t.Fatalf("unexpected codex roots: %#v", codexRoots)
	}
	if !slices.Equal(traeRoots, []string{traeRoot}) {
		t.Fatalf("unexpected trae roots: %#v", traeRoots)
	}
	wantPriority := []TranscriptFile{
		{Tool: "claude", Path: claudeSession},
		{Tool: "codex", Path: codexSession},
		{Tool: "trae", Path: traeSession},
	}
	if !slices.Equal(priority, wantPriority) {
		t.Fatalf("unexpected priority files: %#v", priority)
	}
}
