package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var sessionHintPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)thread-id["=: ]+"?([0-9a-f-]{8,})`),
	regexp.MustCompile(`(?i)sessionid["=: ]+"?([0-9a-f-]{8,})`),
	regexp.MustCompile(`(?i)session_id["=: ]+"?([0-9a-f-]{8,})`),
	regexp.MustCompile(`(?i)(?:--thread-id|--session-id)[= ]([0-9a-f-]{8,})`),
	regexp.MustCompile(`(?i)CODEX_THREAD_ID=([0-9a-f-]{8,})`),
}

func discoverLiveProcesses(ctx context.Context) ([]LiveProcess, []string) {
	out, err := exec.CommandContext(ctx, "ps", "-axo", "uid=,pid=,ppid=,command=").Output()
	if err != nil {
		return nil, []string{"ps failed: " + strings.TrimSpace(err.Error())}
	}

	processTable := parseProcessTable(string(out))
	processes := []LiveProcess{}
	pids := []int{}
	for _, process := range processTable {
		if process.UID != os.Getuid() {
			continue
		}
		tool := detectedTool(process.Command)
		if tool == "" {
			continue
		}
		hostApp := inferHostApp(process, processTable)
		processes = append(processes, LiveProcess{
			PID:     process.PID,
			PPID:    process.PPID,
			Tool:    tool,
			Command: strings.TrimSpace(process.Command),
			HostApp: hostApp,
		})
		pids = append(pids, process.PID)
	}
	sort.Slice(processes, func(i, j int) bool {
		if processes[i].Tool == processes[j].Tool {
			return processes[i].PID < processes[j].PID
		}
		return processes[i].Tool < processes[j].Tool
	})
	fileMap, lsofNotes := sessionFilesForPIDs(ctx, pids)
	for i := range processes {
		processes[i].SessionFiles = fileMap[processes[i].PID]
		processes[i].SessionHints = extractSessionHints(processes[i].Command)
	}
	return processes, lsofNotes
}

var discoverLiveProcessesFunc = discoverLiveProcesses

type processRow struct {
	UID     int
	PID     int
	PPID    int
	Command string
}

func parseProcessTable(output string) map[int]processRow {
	rows := map[int]processRow{}
	for _, line := range strings.Split(output, "\n") {
		row, ok := parseProcessTableLine(line)
		if !ok {
			continue
		}
		rows[row.PID] = row
	}
	return rows
}

func parseProcessTableLine(line string) (processRow, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 4 {
		return processRow{}, false
	}
	uid, err := strconv.Atoi(fields[0])
	if err != nil || uid < 0 {
		return processRow{}, false
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil || pid <= 0 {
		return processRow{}, false
	}
	ppid, err := strconv.Atoi(fields[2])
	if err != nil || ppid < 0 {
		return processRow{}, false
	}
	return processRow{
		UID:     uid,
		PID:     pid,
		PPID:    ppid,
		Command: strings.Join(fields[3:], " "),
	}, true
}

func parsePSLine(line string) (uid, pid int, command string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 3 {
		return 0, 0, "", false
	}
	uid, err := strconv.Atoi(fields[0])
	if err != nil || uid < 0 {
		return 0, 0, "", false
	}
	pid, err = strconv.Atoi(fields[1])
	if err != nil || pid <= 0 {
		return 0, 0, "", false
	}
	return uid, pid, strings.Join(fields[2:], " "), true
}

func inferHostApp(process processRow, processes map[int]processRow) *HostApp {
	current := process
	seen := map[int]struct{}{}
	for steps := 0; steps < 12; steps++ {
		if _, ok := seen[current.PID]; ok {
			return nil
		}
		seen[current.PID] = struct{}{}
		if app := hostAppFromCommand(current.PID, current.Command); app != nil {
			if app.BundlePath == "" {
				return nil
			}
			if info, err := os.Stat(app.BundlePath); err == nil && info.IsDir() {
				return app
			}
		}
		if current.PPID <= 0 {
			return nil
		}
		parent, ok := processes[current.PPID]
		if !ok {
			return nil
		}
		current = parent
	}
	return nil
}

func hostAppFromCommand(pid int, command string) *HostApp {
	bundlePath := appBundlePathFromCommand(command)
	if bundlePath == "" {
		return nil
	}
	name := strings.TrimSuffix(filepath.Base(bundlePath), ".app")
	if name == "" {
		name = filepath.Base(bundlePath)
	}
	return &HostApp{PID: pid, Name: name, BundlePath: bundlePath}
}

func appBundlePathFromCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if command[0] == '"' || command[0] == '\'' {
		quote := command[0]
		if end := strings.IndexByte(command[1:], quote); end >= 0 {
			command = command[1 : end+1]
		}
	}
	if !strings.HasPrefix(command, string(filepath.Separator)) {
		return ""
	}
	index := strings.Index(strings.ToLower(command), ".app")
	if index < 0 {
		return ""
	}
	candidate := strings.Trim(command[:index+len(".app")], `"'`)
	if !filepath.IsAbs(candidate) {
		return ""
	}
	candidate = filepath.Clean(candidate)
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return candidate
	}
	return ""
}

func detectedTool(command string) string {
	lower := strings.ToLower(strings.TrimSpace(command))
	if lower == "" {
		return ""
	}
	if strings.Contains(lower, "sparkle") || strings.Contains(lower, "updater.app") {
		return ""
	}
	fields := strings.Fields(lower)
	executable := ""
	if len(fields) > 0 {
		executable = fields[0]
	}
	executableBase := strings.TrimSuffix(filepath.Base(executable), ".app")
	switch {
	case strings.Contains(executableBase, "claude"):
		return "claude"
	case strings.Contains(executableBase, "traex"),
		executableBase == "trae",
		strings.Contains(executable, "/traex"),
		strings.Contains(executable, "/trae"):
		return "trae"
	case strings.Contains(executableBase, "codexl"),
		strings.Contains(executableBase, "codex"),
		strings.Contains(lower, "/applications/codex.app"),
		strings.Contains(lower, "codex computer use.app"),
		strings.Contains(lower, "com.openai.codex"):
		return "codex"
	default:
		return ""
	}
}

func sessionFilesForPIDs(ctx context.Context, pids []int) (map[int][]TranscriptFile, []string) {
	out := map[int][]TranscriptFile{}
	if len(pids) == 0 {
		return out, nil
	}
	pidText := make([]string, 0, len(pids))
	for _, pid := range pids {
		if pid > 0 {
			pidText = append(pidText, strconv.Itoa(pid))
		}
	}
	if len(pidText) == 0 {
		return out, nil
	}
	cmd := exec.CommandContext(ctx, "lsof", "-nP", "-Fn", "-p", strings.Join(pidText, ","))
	output, err := cmd.CombinedOutput()
	notes := []string{}
	if err != nil && len(output) == 0 {
		return out, []string{"lsof failed: " + strings.TrimSpace(err.Error())}
	}
	if err != nil {
		notes = append(notes, "lsof returned a partial result: "+strings.TrimSpace(err.Error()))
	}
	currentPID := 0
	seen := map[int]map[string]struct{}{}
	for _, raw := range strings.Split(string(output), "\n") {
		if len(raw) < 2 {
			continue
		}
		switch raw[0] {
		case 'p':
			pid, parseErr := strconv.Atoi(strings.TrimSpace(raw[1:]))
			if parseErr != nil || pid <= 0 {
				currentPID = 0
				continue
			}
			currentPID = pid
		case 'n':
			if currentPID == 0 {
				continue
			}
			file, ok := transcriptFileFromPath(strings.TrimSpace(raw[1:]))
			if !ok {
				continue
			}
			if seen[currentPID] == nil {
				seen[currentPID] = map[string]struct{}{}
			}
			key := file.Tool + "\x00" + file.Path
			if _, exists := seen[currentPID][key]; exists {
				continue
			}
			seen[currentPID][key] = struct{}{}
			out[currentPID] = append(out[currentPID], file)
		}
	}
	for pid := range out {
		sort.Slice(out[pid], func(i, j int) bool {
			if out[pid][i].Tool == out[pid][j].Tool {
				return out[pid][i].Path < out[pid][j].Path
			}
			return out[pid][i].Tool < out[pid][j].Tool
		})
	}
	return out, notes
}

func transcriptFileFromPath(path string) (TranscriptFile, bool) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return TranscriptFile{}, false
	}
	switch {
	case strings.Contains(path, string(filepath.Separator)+".claude"+string(filepath.Separator)+"projects"+string(filepath.Separator)) && strings.HasSuffix(path, ".jsonl"):
		return TranscriptFile{Tool: "claude", Path: path}, true
	case strings.Contains(path, string(filepath.Separator)+".codex"+string(filepath.Separator)+"sessions"+string(filepath.Separator)) && strings.HasSuffix(path, ".jsonl"):
		return TranscriptFile{Tool: "codex", Path: path}, true
	case strings.Contains(path, string(filepath.Separator)+".codex"+string(filepath.Separator)+"archived_sessions"+string(filepath.Separator)) && strings.HasSuffix(path, ".jsonl"):
		return TranscriptFile{Tool: "codex", Path: path}, true
	case strings.Contains(path, string(filepath.Separator)+".codex"+string(filepath.Separator)+".codexl"+string(filepath.Separator)) && strings.HasSuffix(path, string(filepath.Separator)+"events.jsonl"):
		return TranscriptFile{Tool: "codex", Path: path}, true
	case strings.Contains(path, string(filepath.Separator)+".trae"+string(filepath.Separator)+"cli"+string(filepath.Separator)+"sessions"+string(filepath.Separator)) && strings.HasSuffix(path, ".jsonl"):
		return TranscriptFile{Tool: "trae", Path: path}, true
	default:
		return TranscriptFile{}, false
	}
}

func extractSessionHints(command string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, pattern := range sessionHintPatterns {
		for _, match := range pattern.FindAllStringSubmatch(command, -1) {
			if len(match) < 2 {
				continue
			}
			value := strings.TrimSpace(match[1])
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
