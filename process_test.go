package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestProcessIOSamplerEvictsPIDsMissingFromBatch(t *testing.T) {
	processIOSampler.Lock()
	defer processIOSampler.Unlock()
	savedPrevious := processIOSampler.previous
	savedBatchAt := processIOSampler.batchAt
	defer func() {
		processIOSampler.previous = savedPrevious
		processIOSampler.batchAt = savedBatchAt
	}()

	t1 := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(30 * time.Second)
	t3 := t2.Add(30 * time.Second)
	processIOSampler.previous = map[int]processIOCounter{
		1: {At: t1},
		2: {At: t1},
	}
	processIOSampler.batchAt = t1

	// A new batch boundary keeps previous-batch entries so rates can still
	// be derived for PIDs the new batch is about to sample.
	rotateProcessIOBatchLocked(t2)
	if len(processIOSampler.previous) != 2 {
		t.Fatalf("rotation at the batch boundary must keep previous-batch entries, got %#v", processIOSampler.previous)
	}
	processIOSampler.previous[1] = processIOCounter{At: t2}

	// Calls within the same batch share the same now and must not evict.
	rotateProcessIOBatchLocked(t2)
	if _, ok := processIOSampler.previous[2]; !ok {
		t.Fatalf("same-batch rotation must not evict entries")
	}

	// The next batch evicts pid 2, which the t2 batch never sampled.
	rotateProcessIOBatchLocked(t3)
	if _, ok := processIOSampler.previous[2]; ok {
		t.Fatalf("pid absent from the previous batch must be evicted")
	}
	if _, ok := processIOSampler.previous[1]; !ok {
		t.Fatalf("pid sampled in the previous batch must stay")
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

func TestParseProcessTableLineIncludesResourceUsage(t *testing.T) {
	row, ok := parseProcessTableLine(`  501  4242  101  12.5  131072  01:02:03 codex --thread-id 123e4567-e89b-12d3-a456-426614174000`)
	if !ok {
		t.Fatalf("expected resource process table line to parse")
	}
	if row.UID != 501 || row.PID != 4242 || row.PPID != 101 {
		t.Fatalf("unexpected row ids: %#v", row)
	}
	if row.CPUPercent != 12.5 {
		t.Fatalf("unexpected cpu percent: %v", row.CPUPercent)
	}
	if row.MemoryBytes != 131072*1024 {
		t.Fatalf("unexpected memory bytes: %d", row.MemoryBytes)
	}
	if row.Elapsed != "01:02:03" {
		t.Fatalf("unexpected elapsed: %q", row.Elapsed)
	}
	if row.Command != `codex --thread-id 123e4567-e89b-12d3-a456-426614174000` {
		t.Fatalf("unexpected command: %q", row.Command)
	}
}

func TestParseProcessTableLineAcceptsLocaleDecimalCPU(t *testing.T) {
	row, ok := parseProcessTableLine(`  501  4242  101  12,5  131072  01:02:03 codex --thread-id 123e4567-e89b-12d3-a456-426614174000`)
	if !ok {
		t.Fatalf("expected locale-formatted process table line to parse")
	}
	if row.CPUPercent != 12.5 {
		t.Fatalf("unexpected locale cpu percent: %v", row.CPUPercent)
	}
	if row.Command != `codex --thread-id 123e4567-e89b-12d3-a456-426614174000` {
		t.Fatalf("locale-formatted row lost command: %q", row.Command)
	}
	if tool, _ := defaultCodingAgentRegistry(Config{}).detectProcess(row.Command); tool != "codex" {
		t.Fatalf("locale-formatted Codex row was not detected: %q", tool)
	}
}

func TestParseProcessTableDoesNotTreatEmptyOrUnparseableOutputAsAValidSample(t *testing.T) {
	for _, output := range []string{"", "not a process row\n", "uid pid ppid pcpu rss etime command\n"} {
		if rows := parseProcessTable(output); len(rows) != 0 {
			t.Fatalf("parseProcessTable(%q) returned rows: %#v", output, rows)
		}
	}
}

func TestDetectedTool(t *testing.T) {
	registry := defaultCodingAgentRegistry(Config{})
	cases := []struct {
		command string
		want    string
	}{
		{command: `/usr/local/bin/codexL as-agent watch`, want: "codex"},
		{command: `/usr/local/bin/traex --yolo resume 019f0abc`, want: "trae"},
		{command: `/usr/local/bin/trae_cli --yolo resume 019f0abc`, want: "trae"},
		{command: `/usr/local/bin/trae-cli --yolo resume 019f0abc`, want: "trae"},
		{command: `/usr/local/bin/trae --yolo resume 019f0abc`, want: "trae"},
		{command: `/usr/local/bin/traefik --config local.yaml`, want: ""},
		{command: `/opt/homebrew/bin/opencode run`, want: "opencode"},
		{command: `/opt/homebrew/bin/opencode-ai run`, want: "opencode"},
		{command: `/opt/homebrew/bin/gemini --prompt hello`, want: "gemini"},
		{command: `/opt/homebrew/bin/gemini-cli --prompt hello`, want: "gemini"},
		{command: `/opt/homebrew/bin/gemini-weather --city tokyo`, want: ""},
		{command: `/usr/local/bin/list-opencode --all`, want: ""},
		{command: `/usr/local/bin/node /opt/homebrew/lib/node_modules/@google/gemini-cli/dist/index.js --prompt hello`, want: "gemini"},
		{command: `/usr/local/bin/node /opt/homebrew/lib/node_modules/opencode-ai/bin/opencode.js run`, want: "opencode"},
		{command: `/usr/local/bin/node local-runner.js --model gemini --agent opencode`, want: ""},
		{command: `/usr/local/bin/hermes chat`, want: "hermes"},
		{command: `/usr/local/bin/hermes-agent --model test`, want: "hermes"},
		{command: `/usr/local/bin/hermes-acp`, want: "hermes"},
		{command: `/opt/hermes/venv/bin/python -m hermes_cli.main gateway run --replace`, want: "hermes"},
		{command: `/opt/hermes/venv/bin/python3.11 -u -m run_agent --model test`, want: ""},
		{command: `/usr/local/bin/hermes-proxy`, want: ""},
		{command: `/usr/bin/python3 local-runner.py --agent hermes`, want: ""},
		{command: `/usr/bin/python3 -m hermes_cli.helper`, want: ""},
		{command: `/Applications/Cursor.app/Contents/MacOS/Cursor`, want: ""},
		{command: `/usr/local/bin/openclaw`, want: ""},
		{command: `/usr/local/bin/pi`, want: ""},
		{command: `/Applications/Codex.app/Contents/MacOS/Codex`, want: "codex"},
		{command: `codex --prompt codex helper`, want: "codex"},
		{command: `codex --plugin fixture/Codex.app/Contents/Frameworks/Codex Helper.app/Contents/MacOS/Codex Helper`, want: "codex"},
		{command: `Codex Computer Use.app/Contents/MacOS/Codex Computer Use`, want: "codex"},
		{command: `fixtures/Codex Computer Use.app/Contents/SharedSupport/SkyComputerUseClient.app/Contents/MacOS/SkyComputerUseClient event-stream mcp`, want: "codex"},
		{command: `/Applications/Claude.app/Contents/MacOS/Claude`, want: "claude"},
		{command: `fixture/Codex.app/Contents/Frameworks/Codex Helper (Renderer).app/Contents/MacOS/Codex Helper (Renderer) --type=renderer`, want: ""},
		{command: `fixture/Codex.app/Contents/Frameworks/Codex Helper.app/Contents/MacOS/Codex Helper`, want: ""},
		{command: `fixture/Codex.app/Contents/Frameworks/Codex Helper.app/Contents/MacOS/Codex Helper event-stream`, want: ""},
		{command: `fixture/Codex.app/Contents/Frameworks/Codex Helper (GPU).app/Contents/MacOS/Codex Helper (GPU) --type=gpu-process`, want: ""},
		{command: `fixture/Codex.app/Contents/Frameworks/Codex Helper.app/Contents/MacOS/Codex Helper --type=utility`, want: ""},
		{command: `fixture/ChatGPT.app/Contents/Frameworks/Codex Framework.framework/Versions/1.0/Helpers/Codex (Renderer).app/Contents/MacOS/Codex (Renderer) --type=renderer`, want: ""},
		{command: `fixture/Codex.app/Contents/Frameworks/crashpad_handler --annotation=_productName=Codex`, want: ""},
		{command: `fixture/Codex.app/Contents/Frameworks/browser_crashpad_handler --annotation=_productName=ChatGPT`, want: ""},
		{command: `fixture/ChatGPT.app/Contents/Frameworks/Codex Framework.framework/Versions/1.0/Helpers/browser_crashpad_handler --monitor-self`, want: ""},
		{command: `fixture/ChatGPT.app/Contents/Frameworks/Codex Framework.framework/Versions/1.0/Helpers/Codex (Service).app/Contents/MacOS/Codex (Service) --type=gpu-process`, want: ""},
		{command: `codex-code-mode-host`, want: ""},
		{command: `fixture/Codex.app/Contents/MacOS/Updater.app --sparkle`, want: ""},
		{command: ``, want: ""},
	}
	for _, tc := range cases {
		got, _ := registry.detectProcess(tc.command)
		if got != tc.want {
			t.Fatalf("detectedTool(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

func TestProcessDiscoveryFailureNoteIsStructured(t *testing.T) {
	if reason, ok := processDiscoveryFailure([]string{"lsof failed: permission denied"}); ok || reason != "" {
		t.Fatalf("lsof-only note must not invalidate process rows: (%q, %v)", reason, ok)
	}
	if reason, ok := processDiscoveryFailure([]string{processDiscoveryFailurePrefix + "signal: killed"}); !ok || reason != "signal: killed" {
		t.Fatalf("unexpected process discovery failure: (%q, %v)", reason, ok)
	}
}

func TestRegistryReturnsAdapterOwnedProcessDisplayIdentity(t *testing.T) {
	registry := defaultCodingAgentRegistry(Config{})
	cases := []struct {
		command string
		tool    string
		display string
	}{
		{command: `/usr/local/bin/codexL as-agent watch`, tool: "codex", display: "codexL"},
		{command: `/usr/local/bin/trae_cli resume abc`, tool: "trae", display: "trae_cli"},
		{command: `/usr/local/bin/node /opt/homebrew/lib/node_modules/opencode-ai/bin/opencode.js`, tool: "opencode", display: "opencode"},
		{command: `/opt/hermes/venv/bin/python -m hermes_cli.main gateway run --replace`, tool: "hermes", display: "hermes"},
	}
	for _, tc := range cases {
		tool, display := registry.detectProcess(tc.command)
		if tool != tc.tool || display != tc.display {
			t.Fatalf("detectProcess(%q) = (%q, %q), want (%q, %q)", tc.command, tool, display, tc.tool, tc.display)
		}
	}
}

func TestLimitedAdaptersExposeOnlyVerifiedCapabilities(t *testing.T) {
	registry := defaultCodingAgentRegistry(Config{})
	for _, agentID := range []string{"gemini", "opencode", "hermes"} {
		index, registered := registry.byID[agentID]
		if !registered || registry.adapters[index].Capabilities.Process == nil {
			t.Fatalf("process-verified adapter %s is not registered with process identity", agentID)
		}
		if registry.hasDiscovery(agentID) {
			t.Fatalf("limited adapter %s unexpectedly exposes discovery", agentID)
		}
		if registry.hasTranscript(agentID) {
			t.Fatalf("limited adapter %s unexpectedly exposes transcript parsing", agentID)
		}
	}
	for _, agentID := range []string{"cursor", "openclaw", "pi"} {
		index, registered := registry.byID[agentID]
		if !registered {
			t.Fatalf("identity-only adapter %s is not registered", agentID)
		}
		if capabilities := registry.adapters[index].Capabilities; capabilities.Process != nil || capabilities.Discovery != nil || capabilities.Transcript != nil || capabilities.Usage != nil {
			t.Fatalf("identity-only adapter %s fabricated capabilities: %+v", agentID, capabilities)
		}
	}
	if file, ok := registry.transcriptFileForPath(filepath.Join("fixtures", ".gemini", "sessions", "session.jsonl")); ok {
		t.Fatalf("process-only adapter fabricated transcript evidence: %#v", file)
	}
}

func TestCursorAppRemainsHostEvidenceWithoutAgentIdentity(t *testing.T) {
	root := t.TempDir()
	bundlePath := filepath.Join(root, "Cursor.app")
	if err := os.MkdirAll(filepath.Join(bundlePath, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	processes := map[int]processRow{
		100: {UID: 501, PID: 100, PPID: 1, Command: filepath.Join(bundlePath, "Contents", "MacOS", "Cursor")},
		200: {UID: 501, PID: 200, PPID: 100, Command: `/usr/local/bin/codex`},
	}
	app := inferHostApp(processes[200], processes)
	if app == nil || app.Name != "Cursor" || app.BundlePath != bundlePath {
		t.Fatalf("Cursor host evidence = %#v", app)
	}
	if tool, _ := defaultCodingAgentRegistry(Config{}).detectProcess(processes[100].Command); tool != "" {
		t.Fatalf("Cursor host process fabricated agent identity %q", tool)
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
	registry := defaultCodingAgentRegistry(Config{})
	cases := []struct {
		path     string
		wantTool string
		wantHint string
		wantOK   bool
	}{
		{path: filepath.Join("fixtures", "alice", ".codex", "sessions", "2026", "06", "28", "abc.jsonl"), wantTool: "codex", wantHint: "abc", wantOK: true},
		{path: filepath.Join("fixtures", "alice", ".codex", "archived_sessions", "abc.jsonl"), wantTool: "codex", wantHint: "abc", wantOK: true},
		{path: filepath.Join("fixtures", "alice", ".codex", ".codexl", "asagent", "lane-1", "events.jsonl"), wantTool: "codex", wantHint: "lane-1", wantOK: true},
		{path: filepath.Join("fixtures", "alice", ".claude", "projects", "project-a", "trace.jsonl"), wantTool: "claude", wantHint: "trace", wantOK: true},
		{path: filepath.Join("fixtures", "alice", ".trae", "cli", "sessions", "2026", "06", "28", "trace.jsonl"), wantTool: "trae", wantHint: "trace", wantOK: true},
		{path: filepath.Join("fixtures", "alice", ".codex", "sessions", "abc.jsonl"), wantTool: "", wantOK: false},
		{path: filepath.Join("fixtures", "alice", ".trae", "cli", "sessions", "2026", "06", "28", "trace.artifacts", "usage.jsonl"), wantTool: "", wantOK: false},
	}
	for _, tc := range cases {
		got, ok := registry.transcriptFileForPath(tc.path)
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
		if got.SessionIDHint != tc.wantHint {
			t.Fatalf("transcriptFileFromPath(%q) session hint = %q, want %q", tc.path, got.SessionIDHint, tc.wantHint)
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

func TestExtractSessionHintsIncludesResumeSessionIDs(t *testing.T) {
	command := `claude --resume abcdef12`
	want := []string{"abcdef12"}
	if got := extractSessionHints(command); !slices.Equal(got, want) {
		t.Fatalf("unexpected resume session hints: %#v", got)
	}
}

func TestResumeSessionHintReachesLiveSessionMapping(t *testing.T) {
	const sessionID = "abcdef12"
	now := time.Unix(0, 0).UTC()
	processes := []LiveProcess{{
		PID:          701,
		Tool:         "claude",
		Command:      `claude --resume ` + sessionID,
		SessionHints: extractSessionHints(`claude --resume ` + sessionID),
	}}

	sessions, notes := buildLiveSessionsAt(processes, &TranscriptData{Traces: map[string]*SessionTrace{}}, 90*time.Second, now)
	if len(notes) != 1 || !slices.Contains(notes, "1 live sessions lack transcript timing, so active burst concurrency is conservative.") {
		t.Fatalf("expected only the untraced-session note, got %#v", notes)
	}
	if len(sessions) != 1 || sessions[0].Tool != "claude" || sessions[0].SessionID != sessionID {
		t.Fatalf("unexpected live session mapping: %#v", sessions)
	}
	if len(sessions[0].Processes) != 1 {
		t.Fatalf("expected one process in resume session mapping, got %#v", sessions[0].Processes)
	}
	if !sessions[0].Mapping.CommandHint || sessions[0].Mapping.ParsedTranscriptID || sessions[0].Mapping.FallbackSessionID {
		t.Fatalf("unexpected resume session mapping provenance: %#v", sessions[0].Mapping)
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

	roots, priority := rootsFromLiveProcesses(processes, defaultCodingAgentRegistry(Config{}))
	if !slices.Equal(roots["claude"], []string{claudeRoot}) {
		t.Fatalf("unexpected claude roots: %#v", roots["claude"])
	}
	if !slices.Equal(roots["codex"], []string{projectCodexRoot, homeCodexRoot}) {
		t.Fatalf("unexpected codex roots: %#v", roots["codex"])
	}
	if !slices.Equal(roots["trae"], []string{traeRoot}) {
		t.Fatalf("unexpected trae roots: %#v", roots["trae"])
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
