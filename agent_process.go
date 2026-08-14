package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type processCommand struct {
	Raw            string
	Lower          string
	Fields         []string
	ExecutableBase string
	Script         string
	Module         string
	Excluded       bool
}

func newProcessCommand(command string) processCommand {
	raw := strings.TrimSpace(command)
	lower := strings.ToLower(raw)
	view := processCommand{
		Raw:      raw,
		Lower:    lower,
		Fields:   strings.Fields(lower),
		Excluded: strings.Contains(lower, "sparkle") || strings.Contains(lower, "updater.app"),
	}
	if len(view.Fields) == 0 {
		return view
	}
	view.ExecutableBase = normalizedExecutableBase(view.Fields[0])
	switch {
	case view.ExecutableBase == "node" || view.ExecutableBase == "bun" || view.ExecutableBase == "deno":
		view.Script = interpreterScriptToken(view.Fields[1:])
	case isPythonExecutable(view.ExecutableBase):
		view.Module = pythonModuleToken(view.Fields[1:])
	}
	return view
}

type builtinProcessIdentity struct {
	agentID            string
	match              func(processCommand) bool
	display            func(processCommand) string
	transcriptPath     func(string) bool
	rootFromTranscript func(string) string
	sessionIDHint      func(string) string
	commandRootPattern *regexp.Regexp
}

func (p builtinProcessIdentity) MatchesCommand(command processCommand) bool {
	return p.match != nil && p.match(command)
}

func (p builtinProcessIdentity) DisplayIdentity(command processCommand) string {
	if p.display != nil {
		if identity := strings.TrimSpace(p.display(command)); identity != "" {
			return identity
		}
	}
	return p.agentID
}

func (p builtinProcessIdentity) TranscriptFileForPath(path string) (TranscriptFile, bool) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || p.transcriptPath == nil || !p.transcriptPath(path) {
		return TranscriptFile{}, false
	}
	file := TranscriptFile{Tool: p.agentID, Path: path}
	if p.sessionIDHint != nil {
		file.SessionIDHint = p.sessionIDHint(path)
	}
	return file, true
}

func (p builtinProcessIdentity) RootFromTranscriptPath(path string) string {
	if p.rootFromTranscript == nil {
		return ""
	}
	return p.rootFromTranscript(path)
}

func (p builtinProcessIdentity) RootsFromCommand(command processCommand) []string {
	if p.commandRootPattern == nil {
		return nil
	}
	return existingCommandRoots(command.Raw, p.commandRootPattern)
}

func newClaudeProcessIdentity() agentProcessIdentity {
	return builtinProcessIdentity{
		agentID: "claude",
		match: func(command processCommand) bool {
			return strings.Contains(command.ExecutableBase, "claude")
		},
		display: func(processCommand) string { return "claude" },
		transcriptPath: func(path string) bool {
			relative, ok := relativeAfterMarker(path, []string{".claude", "projects"})
			if !ok || !strings.HasSuffix(strings.ToLower(relative), ".jsonl") {
				return false
			}
			for _, part := range strings.Split(relative, string(filepath.Separator)) {
				switch strings.ToLower(part) {
				case "memory", "tool-results":
					return false
				}
			}
			return true
		},
		rootFromTranscript: func(path string) string { return configRootFromPath(path, ".claude") },
		sessionIDHint:      genericTranscriptSessionID,
		commandRootPattern: commandRootPattern(".claude"),
	}
}

func newCodexProcessIdentity() agentProcessIdentity {
	return builtinProcessIdentity{
		agentID: "codex",
		match: func(command processCommand) bool {
			if isCodexInternalProcess(command) {
				return false
			}
			return strings.Contains(command.ExecutableBase, "codexl") ||
				strings.Contains(command.ExecutableBase, "codex") ||
				strings.Contains(command.Lower, "/applications/codex.app") ||
				strings.Contains(command.Lower, "codex computer use.app") ||
				strings.Contains(command.Lower, "com.openai.codex")
		},
		display: func(command processCommand) string {
			for _, field := range command.Fields {
				base := strings.ToLower(cleanCommandBase(field))
				if strings.Contains(base, "codexl") {
					return "codexL"
				}
				if base == "codex" || strings.HasPrefix(base, "codex-") {
					return "codex"
				}
			}
			return "codex"
		},
		transcriptPath: func(path string) bool {
			if relative, ok := relativeAfterMarker(path, []string{".codex", "sessions"}); ok {
				return isDatedTranscriptRelativePath(relative)
			}
			if relative, ok := relativeAfterMarker(path, []string{".codex", "archived_sessions"}); ok {
				return !strings.Contains(relative, string(filepath.Separator)) && strings.HasSuffix(strings.ToLower(relative), ".jsonl")
			}
			if relative, ok := relativeAfterMarker(path, []string{".codex", ".codexl"}); ok {
				return strings.Contains(relative, string(filepath.Separator)) && filepath.Base(relative) == "events.jsonl"
			}
			return false
		},
		rootFromTranscript: func(path string) string { return configRootFromPath(path, ".codex") },
		sessionIDHint:      codexTranscriptSessionID,
		commandRootPattern: commandRootPattern(".codex"),
	}
}

func isCodexInternalProcess(command processCommand) bool {
	raw := strings.TrimSpace(command.Raw)
	lowerRaw := strings.ToLower(raw)
	executablePrefix := strings.TrimSpace(strings.SplitN(lowerRaw, " --", 2)[0])
	for _, name := range []string{"browser_crashpad_handler", "crashpad_handler"} {
		if executablePrefix == name || strings.HasSuffix(executablePrefix, string(filepath.Separator)+name) || strings.HasSuffix(executablePrefix, " "+name) {
			return true
		}
	}
	if marker := strings.Index(lowerRaw, "/contents/macos/"); marker >= 0 && !strings.Contains(raw[:marker], " --") {
		executable := strings.TrimSpace(lowerRaw[marker+len("/contents/macos/"):])
		for _, name := range []string{"codex helper", "codex helper (renderer)", "codex helper (gpu)", "codex (renderer)", "codex (gpu)", "codex (service)"} {
			if strings.HasPrefix(executable, name) && (len(executable) == len(name) || executable[len(name)] == ' ' || executable[len(name)] == '\t') {
				return true
			}
		}
	}
	executablePath := commandExecutablePath(command.Raw)
	base := normalizedExecutableBase(executablePath)
	if base == "codex-code-mode-host" || base == "crashpad-handler" || base == "browser-crashpad-handler" {
		return true
	}
	lowerPath := strings.ToLower(executablePath)
	if !strings.Contains(lowerPath, "/contents/frameworks/") {
		return false
	}
	switch base {
	case "codex helper", "codex helper (renderer)", "codex helper (gpu)",
		"codex helper (utility)", "codex (renderer)", "codex (gpu)", "codex (service)":
		return true
	default:
		return false
	}
}

func commandExecutablePath(command string) string {
	raw := strings.TrimSpace(command)
	if raw == "" {
		return ""
	}
	if quote := raw[0]; quote == '\'' || quote == '"' {
		if end := strings.IndexByte(raw[1:], quote); end >= 0 {
			return raw[1 : end+1]
		}
	}
	// macOS may print an unquoted bundle executable containing spaces. Use the
	// stable Contents/MacOS boundary only when it belongs to the command prefix;
	// an option value later in the command must not become the executable.
	lower := strings.ToLower(raw)
	if marker := strings.Index(lower, "/contents/macos/"); marker > 0 && !strings.Contains(raw[:marker], " --") {
		end := marker + len("/contents/macos/")
		if option := strings.Index(raw[end:], " --"); option >= 0 {
			end += option
		}
		return strings.TrimSpace(raw[:end])
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], `"'`)
}

func newTraeProcessIdentity() agentProcessIdentity {
	return builtinProcessIdentity{
		agentID: "trae",
		match: func(command processCommand) bool {
			return isTraeExecutable(command.ExecutableBase)
		},
		display: func(command processCommand) string {
			if len(command.Fields) == 0 {
				return "trae"
			}
			return cleanCommandBase(command.Fields[0])
		},
		transcriptPath: func(path string) bool {
			relative, ok := relativeAfterMarker(path, []string{".trae", "cli", "sessions"})
			return ok && isDatedTranscriptRelativePath(relative)
		},
		rootFromTranscript: traeRootFromPath,
		sessionIDHint:      genericTranscriptSessionID,
		commandRootPattern: commandRootPattern(filepath.Join(".trae", "cli")),
	}
}

func newGeminiProcessIdentity() agentProcessIdentity {
	return builtinProcessIdentity{
		agentID: "gemini",
		match: func(command processCommand) bool {
			return isGeminiExecutable(command.ExecutableBase) ||
				isGeminiExecutable(command.Script) ||
				knownPackagePathAgent(command.Script) == "gemini"
		},
		display: func(processCommand) string { return "gemini" },
	}
}

func newOpenCodeProcessIdentity() agentProcessIdentity {
	return builtinProcessIdentity{
		agentID: "opencode",
		match: func(command processCommand) bool {
			return isOpenCodeExecutable(command.ExecutableBase) ||
				isOpenCodeExecutable(command.Script) ||
				knownPackagePathAgent(command.Script) == "opencode"
		},
		display: func(processCommand) string { return "opencode" },
	}
}

func newHermesProcessIdentity() agentProcessIdentity {
	return builtinProcessIdentity{
		agentID: "hermes",
		match: func(command processCommand) bool {
			return isHermesExecutable(command.ExecutableBase) || command.Module == "hermes_cli.main"
		},
		display: func(processCommand) string { return "hermes" },
	}
}

func normalizedExecutableBase(value string) string {
	key := strings.Trim(strings.ToLower(value), `"'`)
	key = strings.TrimSuffix(filepath.Base(key), ".app")
	key = strings.TrimSuffix(key, ".exe")
	key = strings.ReplaceAll(key, "_", "-")
	return key
}

func isTraeExecutable(executableBase string) bool {
	switch normalizedExecutableBase(executableBase) {
	case "trae", "traex", "trae-cli", "traecli":
		return true
	default:
		return false
	}
}

func isOpenCodeExecutable(executableBase string) bool {
	switch normalizedExecutableBase(executableBase) {
	case "opencode", "opencode-ai":
		return true
	default:
		return false
	}
}

func isGeminiExecutable(executableBase string) bool {
	switch normalizedExecutableBase(executableBase) {
	case "gemini", "gemini-cli":
		return true
	default:
		return false
	}
}

func isHermesExecutable(executableBase string) bool {
	switch normalizedExecutableBase(executableBase) {
	case "hermes", "hermes-agent", "hermes-acp":
		return true
	default:
		return false
	}
}

func isPythonExecutable(executableBase string) bool {
	executableBase = normalizedExecutableBase(executableBase)
	return executableBase == "python" || executableBase == "python3" || strings.HasPrefix(executableBase, "python3.")
}

func pythonModuleToken(args []string) string {
	optionsWithValue := map[string]struct{}{"-w": {}, "-x": {}}
	for index := 0; index < len(args); index++ {
		arg := strings.Trim(args[index], `"'`)
		if arg == "" {
			continue
		}
		if arg == "-m" {
			if index+1 < len(args) {
				return strings.ToLower(strings.Trim(args[index+1], `"'`))
			}
			return ""
		}
		if _, ok := optionsWithValue[arg]; ok {
			index++
			continue
		}
		if arg == "-c" || !strings.HasPrefix(arg, "-") {
			return ""
		}
	}
	return ""
}

func interpreterScriptToken(args []string) string {
	optionsWithValue := map[string]struct{}{
		"-r": {}, "--require": {}, "--import": {}, "--loader": {},
		"--experimental-loader": {}, "--env-file": {},
	}
	for index := 0; index < len(args); index++ {
		arg := strings.Trim(args[index], `"'`)
		if arg == "" || arg == "--" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			option := arg
			if equals := strings.Index(option, "="); equals >= 0 {
				option = option[:equals]
			}
			if _, ok := optionsWithValue[option]; ok && !strings.Contains(arg, "=") {
				index++
			}
			continue
		}
		return arg
	}
	return ""
}

func knownPackagePathAgent(path string) string {
	normalized := "/" + strings.Trim(strings.ReplaceAll(strings.Trim(path, `"'`), "\\", "/"), "/")
	switch {
	case strings.Contains(normalized, "/node_modules/@google/gemini-cli/"),
		strings.Contains(normalized, "/@google/gemini-cli/"):
		return "gemini"
	case strings.Contains(normalized, "/node_modules/opencode-ai/"),
		strings.Contains(normalized, "/opencode-ai/"):
		return "opencode"
	default:
		return ""
	}
}

func relativeAfterMarker(path string, markerParts []string) (string, bool) {
	path = filepath.Clean(strings.TrimSpace(path))
	marker := string(filepath.Separator) + filepath.Join(markerParts...) + string(filepath.Separator)
	index := strings.Index(path, marker)
	if index < 0 {
		return "", false
	}
	relative := strings.TrimPrefix(path[index+len(marker):], string(filepath.Separator))
	return relative, relative != ""
}

func isDatedTranscriptRelativePath(relative string) bool {
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	if len(parts) != 4 || !strings.HasSuffix(strings.ToLower(parts[3]), ".jsonl") {
		return false
	}
	_, ok := datePartitionEnd(parts[:3], time.UTC)
	return ok
}

func commandRootPattern(suffix string) *regexp.Regexp {
	return regexp.MustCompile(`(/[^ "'\n]+/` + regexp.QuoteMeta(filepath.ToSlash(suffix)) + `)\b`)
}

func existingCommandRoots(command string, pattern *regexp.Regexp) []string {
	seen := map[string]struct{}{}
	roots := []string{}
	for _, match := range pattern.FindAllStringSubmatch(filepath.ToSlash(command), -1) {
		if len(match) < 2 {
			continue
		}
		root := filepath.Clean(match[1])
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}
