package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type discoveredTranscriptFile struct {
	File TranscriptFile
	Info os.FileInfo
}

type transcriptDiscoveryResult struct {
	Files             []discoveredTranscriptFile
	Errors            []string
	VisitedEntries    int
	PrunedDirectories int
}

type transcriptDiscoveryCapability interface {
	Discover(ctx context.Context, agentID string, roots []string, cutoff time.Time) transcriptDiscoveryResult
	Classify(agentID string, roots []string, path string) (TranscriptFile, bool)
}

type claudeTranscriptDiscovery struct{}

func (claudeTranscriptDiscovery) Discover(ctx context.Context, agentID string, roots []string, cutoff time.Time) transcriptDiscoveryResult {
	result := transcriptDiscoveryResult{}
	for _, root := range roots {
		result.merge(walkEvidenceTree(ctx, filepath.Join(root, "projects"), agentID, cutoff,
			func(_ string, entry fs.DirEntry) directoryDecision {
				switch strings.ToLower(entry.Name()) {
				case "memory", "tool-results":
					return pruneDirectory
				default:
					return descendDirectory
				}
			},
			func(path string, _ fs.DirEntry) bool {
				return strings.HasSuffix(strings.ToLower(path), ".jsonl")
			},
		))
	}
	return result
}

func (claudeTranscriptDiscovery) Classify(agentID string, roots []string, path string) (TranscriptFile, bool) {
	for _, root := range roots {
		base := filepath.Join(root, "projects")
		relative, ok := relativeEvidencePath(base, path)
		if !ok || !strings.HasSuffix(strings.ToLower(relative), ".jsonl") {
			continue
		}
		blocked := false
		for _, part := range strings.Split(relative, string(filepath.Separator)) {
			switch strings.ToLower(part) {
			case "memory", "tool-results":
				blocked = true
			}
		}
		if !blocked {
			return TranscriptFile{Tool: agentID, Path: filepath.Clean(filepath.Join(base, relative))}, true
		}
	}
	return TranscriptFile{}, false
}

type codexTranscriptDiscovery struct{}

func (codexTranscriptDiscovery) Discover(ctx context.Context, agentID string, roots []string, cutoff time.Time) transcriptDiscoveryResult {
	result := transcriptDiscoveryResult{}
	for _, root := range roots {
		result.merge(walkEvidenceTree(ctx, filepath.Join(root, "sessions"), agentID, cutoff,
			strictDatePartitionPolicy(cutoff, nil), jsonlFilePolicy))
		result.merge(walkEvidenceTree(ctx, filepath.Join(root, "archived_sessions"), agentID, cutoff,
			flatDirectoryPolicy("codex archived session"), jsonlFilePolicy))
		result.merge(walkEvidenceTree(ctx, filepath.Join(root, ".codexl"), agentID, cutoff,
			func(string, fs.DirEntry) directoryDecision { return descendDirectory },
			func(_ string, entry fs.DirEntry) bool { return entry.Name() == "events.jsonl" },
		))
	}
	return result
}

func (codexTranscriptDiscovery) Classify(agentID string, roots []string, path string) (TranscriptFile, bool) {
	for _, root := range roots {
		base := filepath.Join(root, "sessions")
		if relative, ok := relativeEvidencePath(base, path); ok && isDatedTranscriptRelativePath(relative) {
			return TranscriptFile{Tool: agentID, Path: filepath.Clean(filepath.Join(base, relative))}, true
		}
		base = filepath.Join(root, "archived_sessions")
		if relative, ok := relativeEvidencePath(base, path); ok &&
			!strings.Contains(relative, string(filepath.Separator)) && strings.HasSuffix(strings.ToLower(relative), ".jsonl") {
			return TranscriptFile{Tool: agentID, Path: filepath.Clean(filepath.Join(base, relative))}, true
		}
		base = filepath.Join(root, ".codexl")
		if relative, ok := relativeEvidencePath(base, path); ok &&
			strings.Contains(relative, string(filepath.Separator)) && filepath.Base(relative) == "events.jsonl" {
			return TranscriptFile{Tool: agentID, Path: filepath.Clean(filepath.Join(base, relative))}, true
		}
	}
	return TranscriptFile{}, false
}

type traeTranscriptDiscovery struct{}

func (traeTranscriptDiscovery) Discover(ctx context.Context, agentID string, roots []string, cutoff time.Time) transcriptDiscoveryResult {
	result := transcriptDiscoveryResult{}
	for _, root := range roots {
		result.merge(walkEvidenceTree(ctx, filepath.Join(root, "sessions"), agentID, cutoff,
			strictDatePartitionPolicy(cutoff, func(entry fs.DirEntry) bool {
				return strings.HasSuffix(strings.ToLower(entry.Name()), ".artifacts")
			}), jsonlFilePolicy))
	}
	return result
}

func (traeTranscriptDiscovery) Classify(agentID string, roots []string, path string) (TranscriptFile, bool) {
	for _, root := range roots {
		base := filepath.Join(root, "sessions")
		relative, ok := relativeEvidencePath(base, path)
		if !ok || !isDatedTranscriptRelativePath(relative) {
			continue
		}
		blocked := false
		for _, part := range strings.Split(relative, string(filepath.Separator)) {
			if strings.HasSuffix(strings.ToLower(part), ".artifacts") {
				blocked = true
				break
			}
		}
		if !blocked {
			return TranscriptFile{Tool: agentID, Path: filepath.Clean(filepath.Join(base, relative))}, true
		}
	}
	return TranscriptFile{}, false
}

type antigravityTranscriptDiscovery struct{}

func (antigravityTranscriptDiscovery) Discover(ctx context.Context, agentID string, roots []string, cutoff time.Time) transcriptDiscoveryResult {
	result := transcriptDiscoveryResult{}
	for _, root := range roots {
		brain := filepath.Join(root, "brain")
		discovered := walkEvidenceTree(ctx, brain, agentID, cutoff,
			antigravityBrainDirectoryPolicy, antigravityTranscriptFilePolicy)
		for index := range discovered.Files {
			discovered.Files[index].File.SessionIDHint = antigravityTranscriptSessionID(discovered.Files[index].File.Path)
		}
		result.merge(discovered)
	}
	return result
}

func (antigravityTranscriptDiscovery) Classify(agentID string, roots []string, path string) (TranscriptFile, bool) {
	for _, root := range roots {
		base := filepath.Join(root, "brain")
		relative, ok := relativeEvidencePath(base, path)
		if !ok || !isAntigravityTranscriptRelative(relative) {
			continue
		}
		clean := filepath.Clean(filepath.Join(base, relative))
		return TranscriptFile{
			Tool:          agentID,
			Path:          clean,
			SessionIDHint: antigravityTranscriptSessionID(clean),
		}, true
	}
	return TranscriptFile{}, false
}

func antigravityBrainDirectoryPolicy(relative string, _ fs.DirEntry) directoryDecision {
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) == 0 {
		return pruneDirectory
	}
	if !isAntigravityConversationID(parts[0]) {
		return directoryDecision{Gap: "expected conversation UUID"}
	}
	switch len(parts) {
	case 1:
		return descendDirectory
	case 2:
		switch strings.ToLower(parts[1]) {
		case ".system_generated":
			return descendDirectory
		case "scratch":
			return pruneDirectory
		default:
			return directoryDecision{Gap: "unsupported directory in conversation root"}
		}
	case 3:
		if strings.EqualFold(parts[1], ".system_generated") {
			switch strings.ToLower(parts[2]) {
			case "logs":
				return descendDirectory
			case "steps":
				return pruneDirectory
			}
		}
		return directoryDecision{Gap: "unsupported directory below conversation"}
	default:
		return directoryDecision{Gap: "unsupported directory below logs"}
	}
}

func antigravityTranscriptFilePolicy(path string, entry fs.DirEntry) bool {
	if entry != nil && !strings.EqualFold(entry.Name(), "transcript.jsonl") {
		return false
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	if len(parts) < 4 {
		return false
	}
	relative := filepath.Join(parts[len(parts)-4], parts[len(parts)-3], parts[len(parts)-2], parts[len(parts)-1])
	return isAntigravityTranscriptRelative(relative)
}

func (r *transcriptDiscoveryResult) merge(other transcriptDiscoveryResult) {
	r.Files = append(r.Files, other.Files...)
	r.Errors = append(r.Errors, other.Errors...)
	r.VisitedEntries += other.VisitedEntries
	r.PrunedDirectories += other.PrunedDirectories
}

type directoryDecision struct {
	Descend bool
	Gap     string
}

var (
	descendDirectory = directoryDecision{Descend: true}
	pruneDirectory   = directoryDecision{}
)

type directoryPolicy func(relativePath string, entry fs.DirEntry) directoryDecision
type evidenceFilePolicy func(path string, entry fs.DirEntry) bool

func walkEvidenceTree(ctx context.Context, root, agentID string, cutoff time.Time, directories directoryPolicy, files evidenceFilePolicy) transcriptDiscoveryResult {
	result := transcriptDiscoveryResult{}
	info, err := os.Stat(root)
	if err != nil {
		if !os.IsNotExist(err) {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", root, err))
		}
		return result
	}
	if !info.IsDir() {
		return result
	}

	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, entryErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		result.VisitedEntries++
		if entryErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", path, entryErr))
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry == nil {
			return nil
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", path, relErr))
				return filepath.SkipDir
			}
			decision := directories(relative, entry)
			if decision.Descend {
				return nil
			}
			result.PrunedDirectories++
			if decision.Gap != "" {
				result.Errors = append(result.Errors, fmt.Sprintf("%s evidence layout gap at %s: %s", agentID, path, decision.Gap))
			}
			return filepath.SkipDir
		}
		if files == nil || !files(path, entry) {
			return nil
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", path, infoErr))
			return nil
		}
		if !cutoff.IsZero() && entryInfo.ModTime().Before(cutoff) {
			return nil
		}
		result.Files = append(result.Files, discoveredTranscriptFile{
			File: TranscriptFile{Tool: agentID, Path: filepath.Clean(path)},
			Info: entryInfo,
		})
		return nil
	})
	if walkErr != nil && !isContextError(walkErr) {
		result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", root, walkErr))
	}
	return result
}

func jsonlFilePolicy(path string, _ fs.DirEntry) bool {
	return strings.HasSuffix(strings.ToLower(path), ".jsonl")
}

func relativeEvidencePath(root, path string) (string, bool) {
	root = canonicalEvidencePath(root)
	path = canonicalEvidencePath(path)
	if root == "" || path == "" {
		return "", false
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

func flatDirectoryPolicy(layout string) directoryPolicy {
	return func(_ string, _ fs.DirEntry) directoryDecision {
		return directoryDecision{Gap: "unsupported nested " + layout + " directory"}
	}
}

func strictDatePartitionPolicy(cutoff time.Time, knownExcluded func(fs.DirEntry) bool) directoryPolicy {
	return func(relative string, entry fs.DirEntry) directoryDecision {
		if knownExcluded != nil && knownExcluded(entry) {
			return pruneDirectory
		}
		parts := strings.Split(relative, string(filepath.Separator))
		if len(parts) > 3 {
			return directoryDecision{Gap: "unsupported directory below YYYY/MM/DD partition"}
		}
		partitionEnd, ok := datePartitionEnd(parts, cutoff.Location())
		if !ok {
			return directoryDecision{Gap: "expected YYYY/MM/DD partition"}
		}
		if !cutoff.IsZero() && !partitionEnd.After(cutoff) {
			return pruneDirectory
		}
		return descendDirectory
	}
}

func datePartitionEnd(parts []string, location *time.Location) (time.Time, bool) {
	if len(parts) < 1 || len(parts) > 3 || len(parts[0]) != 4 {
		return time.Time{}, false
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil {
		return time.Time{}, false
	}
	month := 1
	day := 1
	if len(parts) >= 2 {
		if len(parts[1]) != 2 {
			return time.Time{}, false
		}
		month, err = strconv.Atoi(parts[1])
		if err != nil || month < 1 || month > 12 {
			return time.Time{}, false
		}
	}
	if len(parts) == 3 {
		if len(parts[2]) != 2 {
			return time.Time{}, false
		}
		day, err = strconv.Atoi(parts[2])
		if err != nil {
			return time.Time{}, false
		}
	}
	start := time.Date(year, time.Month(month), day, 0, 0, 0, 0, location)
	if start.Year() != year || int(start.Month()) != month || start.Day() != day {
		return time.Time{}, false
	}
	switch len(parts) {
	case 1:
		return start.AddDate(1, 0, 0), true
	case 2:
		return start.AddDate(0, 1, 0), true
	default:
		return start.AddDate(0, 0, 1), true
	}
}
