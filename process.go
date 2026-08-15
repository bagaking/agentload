package main

import (
	"agentload/internal/snapshot"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const processDiscoveryFailurePrefix = "process discovery failed: "

var sessionHintPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)thread-id["=: ]+"?([0-9a-f-]{8,})`),
	regexp.MustCompile(`(?i)sessionid["=: ]+"?([0-9a-f-]{8,})`),
	regexp.MustCompile(`(?i)session_id["=: ]+"?([0-9a-f-]{8,})`),
	regexp.MustCompile(`(?i)(?:--resume|--thread-id|--session-id)[= ]([0-9a-f-]{8,})`),
	// codex and trae take the session as a bare subcommand argument
	// ("codex resume <uuid>"), not a flag. Anchored on a full uuid so an
	// unrelated word after "resume" cannot be read as a session.
	regexp.MustCompile(`(?i)\bresume[= ]+([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b`),
	regexp.MustCompile(`(?i)CODEX_THREAD_ID=([0-9a-f-]{8,})`),
}

func discoverLiveProcesses(ctx context.Context, adapters *codingAgentRegistry) ([]snapshot.LiveProcess, []string) {
	out, err := exec.CommandContext(ctx, "ps", "-axo", "uid=,pid=,ppid=,pcpu=,rss=,etime=,command=").Output()
	if err != nil {
		return nil, []string{processDiscoveryFailurePrefix + strings.TrimSpace(err.Error())}
	}

	processTable := parseProcessTable(string(out))
	if len(processTable) == 0 {
		return nil, []string{processDiscoveryFailurePrefix + "no parseable process rows"}
	}
	now := time.Now()
	processes := []snapshot.LiveProcess{}
	pids := []int{}
	for _, process := range processTable {
		if process.UID != os.Getuid() {
			continue
		}
		tool, displayName := adapters.detectProcess(process.Command)
		if tool == "" {
			continue
		}
		hostApp := inferHostApp(process, processTable)
		ioSample := sampleProcessIO(process.PID, now)
		processes = append(processes, snapshot.LiveProcess{
			PID:                  process.PID,
			PPID:                 process.PPID,
			Tool:                 tool,
			DisplayName:          displayName,
			Command:              strings.TrimSpace(process.Command),
			HostApp:              hostApp,
			CPUPercent:           process.CPUPercent,
			MemoryBytes:          process.MemoryBytes,
			DiskReadBytes:        ioSample.ReadBytes,
			DiskWriteBytes:       ioSample.WriteBytes,
			DiskReadBytesPerSec:  ioSample.ReadBytesPerSec,
			DiskWriteBytesPerSec: ioSample.WriteBytesPerSec,
			Elapsed:              process.Elapsed,
		})
		pids = append(pids, process.PID)
	}
	sort.Slice(processes, func(i, j int) bool {
		if processes[i].Tool == processes[j].Tool {
			return processes[i].PID < processes[j].PID
		}
		return processes[i].Tool < processes[j].Tool
	})
	fileMap, lsofNotes := sessionFilesForPIDs(ctx, pids, adapters)
	for i := range processes {
		processes[i].SessionFiles = fileMap[processes[i].PID]
		processes[i].SessionHints = extractSessionHints(processes[i].Command)
	}
	return processes, lsofNotes
}

func processDiscoveryFailure(notes []string) (string, bool) {
	for _, note := range notes {
		if strings.HasPrefix(note, processDiscoveryFailurePrefix) {
			return strings.TrimSpace(strings.TrimPrefix(note, processDiscoveryFailurePrefix)), true
		}
	}
	return "", false
}

func cloneLiveProcesses(processes []snapshot.LiveProcess) []snapshot.LiveProcess {
	if len(processes) == 0 {
		return nil
	}
	out := make([]snapshot.LiveProcess, len(processes))
	for i, process := range processes {
		out[i] = process
		out[i].SessionFiles = append([]snapshot.TranscriptFile(nil), process.SessionFiles...)
		out[i].SessionHints = append([]string(nil), process.SessionHints...)
		if process.HostApp != nil {
			host := *process.HostApp
			out[i].HostApp = &host
		}
	}
	return out
}

var discoverLiveProcessesFunc = discoverLiveProcesses

type processRow struct {
	UID         int
	PID         int
	PPID        int
	CPUPercent  float64
	MemoryBytes int64
	Elapsed     string
	Command     string
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
	if len(fields) >= 7 {
		if cpuPercent, cpuErr := strconv.ParseFloat(strings.ReplaceAll(fields[3], ",", "."), 64); cpuErr == nil {
			if rssKB, rssErr := strconv.ParseInt(fields[4], 10, 64); rssErr == nil && rssKB >= 0 {
				return processRow{
					UID:         uid,
					PID:         pid,
					PPID:        ppid,
					CPUPercent:  cpuPercent,
					MemoryBytes: rssKB * 1024,
					Elapsed:     fields[5],
					Command:     strings.Join(fields[6:], " "),
				}, true
			}
		}
	}
	return processRow{
		UID:     uid,
		PID:     pid,
		PPID:    ppid,
		Command: strings.Join(fields[3:], " "),
	}, true
}

func inferHostApp(process processRow, processes map[int]processRow) *snapshot.HostApp {
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

func hostAppFromCommand(pid int, command string) *snapshot.HostApp {
	bundlePath := appBundlePathFromCommand(command)
	if bundlePath == "" {
		return nil
	}
	name := strings.TrimSuffix(filepath.Base(bundlePath), ".app")
	if name == "" {
		name = filepath.Base(bundlePath)
	}
	return &snapshot.HostApp{PID: pid, Name: name, BundlePath: bundlePath}
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

func sessionFilesForPIDs(ctx context.Context, pids []int, adapters *codingAgentRegistry) (map[int][]snapshot.TranscriptFile, []string) {
	out := map[int][]snapshot.TranscriptFile{}
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
			file, ok := adapters.transcriptFileForPath(strings.TrimSpace(raw[1:]))
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
