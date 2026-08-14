package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentload/internal/historyfile"
)

const (
	throughputHistorySchemaVersion = 1
	throughputMinuteResolution     = time.Minute
	throughputLegacyMigrationBatch = 256
	throughputHistoryKindMinute    = "minute_fact"
	throughputHistoryKindLegacy    = "legacy_rolling_rate"
)

type ThroughputMinuteProjectFact struct {
	Project       string   `json:"project"`
	OutputTokens  int64    `json:"output_tokens"`
	SessionHashes []string `json:"session_hashes,omitempty"`
}

type ThroughputMinuteFact struct {
	At                string `json:"at"`
	State             string `json:"state"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	// Coverage carries the live sample's floor marker into history: a minute
	// measured from a subset of eligible transcripts is a lower bound, and
	// persisting it as an exact number would launder away that qualifier.
	Coverage      string                        `json:"coverage,omitempty"`
	OutputTokens  *int64                        `json:"output_tokens,omitempty"`
	SessionHashes []string                      `json:"session_hashes,omitempty"`
	Projects      []ThroughputMinuteProjectFact `json:"projects"`
}

type LegacyThroughputFact struct {
	At                    string                       `json:"at"`
	State                 string                       `json:"state"`
	WindowSeconds         int                          `json:"window_seconds"`
	OutputTokensPerSecond *float64                     `json:"output_tokens_per_second,omitempty"`
	ActiveSessions        int                          `json:"active_sessions"`
	Projects              []LiveTokenRateProjectSample `json:"projects"`
}

type throughputHistoryRecord struct {
	SchemaVersion int                   `json:"schema_version"`
	Kind          string                `json:"kind"`
	Minute        *ThroughputMinuteFact `json:"minute,omitempty"`
	Legacy        *LegacyThroughputFact `json:"legacy,omitempty"`
}

type throughputHistoryStore struct {
	mu sync.RWMutex

	path               string
	minutes            []ThroughputMinuteFact
	legacy             []LegacyThroughputFact
	loadedRecordCount  int
	droppedRecordCount int
	corruptRecordCount int
	lastWriteError     string
}

func throughputHistoryPath(historyPath string) string {
	historyPath = resolveHistoryFile(historyPath)
	if strings.TrimSpace(historyPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(historyPath), "throughput.jsonl")
}

func loadThroughputHistoryStore(historyPath string, now time.Time) (*throughputHistoryStore, error) {
	store := &throughputHistoryStore{path: throughputHistoryPath(historyPath)}
	if store.path == "" {
		return store, nil
	}
	lock, err := historyfile.Acquire(store.path)
	if err != nil {
		return store, err
	}
	defer lock.Release()
	file, err := os.Open(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return store, err
	}
	defer file.Close()

	cutoff := now.Add(-historyRetentionWindow)
	minutes := map[string]ThroughputMinuteFact{}
	legacy := map[string]LegacyThroughputFact{}

	// Cold records live in month partitions beside the hot file. Only partitions
	// that can still hold retained records are opened.
	partitions, err := archivePartitionsSince(store.path, cutoff)
	if err != nil {
		return store, err
	}
	archived, err := readArchiveLines(partitions)
	if err != nil {
		return store, err
	}
	for _, line := range archived {
		store.consumeThroughputLine(line, cutoff, minutes, legacy)
	}

	hotLines := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		hotLines++
		store.consumeThroughputLine([]byte(line), cutoff, minutes, legacy)
	}
	if err := scanner.Err(); err != nil {
		return store, err
	}
	for _, minute := range minutes {
		store.minutes = append(store.minutes, minute)
	}
	for _, fact := range legacy {
		store.legacy = append(store.legacy, fact)
	}
	sortThroughputHistory(store.minutes, store.legacy)
	if historyFileNeedsCompaction(hotLines, store.hotRecordCount(now)) {
		if err := compactThroughputHistoryFile(store.path, store.minutes, store.legacy, now); err != nil {
			return store, err
		}
	}
	return store, nil
}

// consumeThroughputLine folds one stored record into the retained maps. Archived
// and hot lines take the same path, and both key on the record's own timestamp,
// so a row a crash left in both places is merged rather than double counted.
func (store *throughputHistoryStore) consumeThroughputLine(line []byte, cutoff time.Time, minutes map[string]ThroughputMinuteFact, legacy map[string]LegacyThroughputFact) {
	var record throughputHistoryRecord
	if err := json.Unmarshal(line, &record); err != nil || record.SchemaVersion != throughputHistorySchemaVersion {
		store.corruptRecordCount++
		return
	}
	store.loadedRecordCount++
	switch record.Kind {
	case throughputHistoryKindMinute:
		minute, at, ok := normalizeThroughputMinute(record.Minute)
		if !ok {
			store.corruptRecordCount++
			return
		}
		if at.Before(cutoff) {
			store.droppedRecordCount++
			return
		}
		minutes[minute.At] = minute
	case throughputHistoryKindLegacy:
		fact, at, ok := normalizeLegacyThroughput(record.Legacy)
		if !ok {
			store.corruptRecordCount++
			return
		}
		if at.Before(cutoff) {
			store.droppedRecordCount++
			return
		}
		legacy[legacyThroughputKey(fact)] = fact
	default:
		store.corruptRecordCount++
	}
}

// hotRecordCount counts retained records inside the hot window, which is what
// the hot file holds after compaction.
func (store *throughputHistoryStore) hotRecordCount(now time.Time) int {
	hotCutoff := now.Add(-historyHotWindow)
	count := 0
	for _, minute := range store.minutes {
		if at, err := time.Parse(time.RFC3339, minute.At); err == nil && !at.Before(hotCutoff) {
			count++
		}
	}
	for _, fact := range store.legacy {
		if at, ok := parseObservedTime(fact.At); ok && !at.Before(hotCutoff) {
			count++
		}
	}
	return count
}

// compactThroughputHistoryFile archives records older than the hot window and
// then rewrites the hot file with the remainder. The archive is made durable
// first so a crash in between duplicates a record rather than losing it.
func compactThroughputHistoryFile(path string, minutes []ThroughputMinuteFact, legacy []LegacyThroughputFact, now time.Time) error {
	hotCutoff := now.Add(-historyHotWindow)
	cold := make([]archiveRow, 0, len(minutes)+len(legacy))
	hotMinutes := make([]ThroughputMinuteFact, 0, len(minutes))
	hotLegacy := make([]LegacyThroughputFact, 0, len(legacy))

	for i := range minutes {
		at, err := time.Parse(time.RFC3339, minutes[i].At)
		if err != nil {
			continue
		}
		if !at.Before(hotCutoff) {
			hotMinutes = append(hotMinutes, minutes[i])
			continue
		}
		minute := cloneThroughputMinute(minutes[i])
		raw, err := json.Marshal(throughputHistoryRecord{SchemaVersion: throughputHistorySchemaVersion, Kind: throughputHistoryKindMinute, Minute: &minute})
		if err != nil {
			return err
		}
		cold = append(cold, archiveRow{At: at, Line: raw})
	}
	for i := range legacy {
		at, ok := parseObservedTime(legacy[i].At)
		if !ok {
			continue
		}
		if !at.Before(hotCutoff) {
			hotLegacy = append(hotLegacy, legacy[i])
			continue
		}
		fact := cloneLegacyThroughput(legacy[i])
		raw, err := json.Marshal(throughputHistoryRecord{SchemaVersion: throughputHistorySchemaVersion, Kind: throughputHistoryKindLegacy, Legacy: &fact})
		if err != nil {
			return err
		}
		cold = append(cold, archiveRow{At: at, Line: raw})
	}
	if err := archiveColdRows(path, cold); err != nil {
		return err
	}
	return rewriteThroughputHistoryFile(path, hotMinutes, hotLegacy)
}


func normalizeThroughputMinute(raw *ThroughputMinuteFact) (ThroughputMinuteFact, time.Time, bool) {
	if raw == nil {
		return ThroughputMinuteFact{}, time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, raw.At)
	if err != nil || !at.Equal(at.Truncate(throughputMinuteResolution)) {
		return ThroughputMinuteFact{}, time.Time{}, false
	}
	minute := cloneThroughputMinute(*raw)
	minute.At = at.UTC().Format(time.RFC3339Nano)
	minute.State = strings.TrimSpace(minute.State)
	minute.Coverage = strings.TrimSpace(minute.Coverage)
	if minute.Coverage != "" && minute.Coverage != liveTokenRateCoveragePartial {
		minute.Coverage = ""
	}
	if minute.State == "" || (minute.OutputTokens != nil && *minute.OutputTokens < 0) {
		return ThroughputMinuteFact{}, time.Time{}, false
	}
	if minute.OutputTokens == nil {
		if minute.State == liveTokenRateStateLive || minute.State == liveTokenRateStateZero {
			return ThroughputMinuteFact{}, time.Time{}, false
		}
		minute.Projects = nil
		minute.SessionHashes = nil
	} else {
		if (*minute.OutputTokens == 0) != (minute.State == liveTokenRateStateZero) || (*minute.OutputTokens > 0) != (minute.State == liveTokenRateStateLive) || minute.Projects == nil {
			return ThroughputMinuteFact{}, time.Time{}, false
		}
		var projectTokens int64
		seenProjects := map[string]struct{}{}
		for i := range minute.Projects {
			project := &minute.Projects[i]
			project.Project = strings.TrimSpace(project.Project)
			if project.Project == "" || project.OutputTokens <= 0 || project.OutputTokens > *minute.OutputTokens-projectTokens {
				return ThroughputMinuteFact{}, time.Time{}, false
			}
			if _, exists := seenProjects[project.Project]; exists {
				return ThroughputMinuteFact{}, time.Time{}, false
			}
			seenProjects[project.Project] = struct{}{}
			projectTokens += project.OutputTokens
		}
		if projectTokens != *minute.OutputTokens {
			return ThroughputMinuteFact{}, time.Time{}, false
		}
		sort.Strings(minute.SessionHashes)
		sort.Slice(minute.Projects, func(i, j int) bool { return minute.Projects[i].Project < minute.Projects[j].Project })
	}
	return minute, at, true
}

func normalizeLegacyThroughput(raw *LegacyThroughputFact) (LegacyThroughputFact, time.Time, bool) {
	if raw == nil || raw.WindowSeconds <= 0 {
		return LegacyThroughputFact{}, time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, raw.At)
	if err != nil {
		return LegacyThroughputFact{}, time.Time{}, false
	}
	fact := cloneLegacyThroughput(*raw)
	fact.At = at.UTC().Format(time.RFC3339Nano)
	if fact.OutputTokensPerSecond == nil {
		fact.Projects = nil
	}
	return fact, at, true
}

func (store *throughputHistoryStore) appendMinute(minute ThroughputMinuteFact, observedAt time.Time) error {
	if store == nil {
		return errors.New("throughput history store is nil")
	}
	if observedAt.IsZero() {
		return errors.New("throughput observation time is required")
	}
	normalized, _, ok := normalizeThroughputMinute(&minute)
	if !ok {
		return errors.New("invalid throughput minute fact")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if index := searchThroughputMinute(store.minutes, normalized.At); index < len(store.minutes) && store.minutes[index].At == normalized.At {
		return nil
	}
	record := throughputHistoryRecord{SchemaVersion: throughputHistorySchemaVersion, Kind: throughputHistoryKindMinute, Minute: &normalized}
	if err := appendThroughputHistoryRecords(store.path, []throughputHistoryRecord{record}); err != nil {
		store.lastWriteError = err.Error()
		return err
	}
	index := searchThroughputMinute(store.minutes, normalized.At)
	store.minutes = append(store.minutes, ThroughputMinuteFact{})
	copy(store.minutes[index+1:], store.minutes[index:])
	store.minutes[index] = normalized
	store.loadedRecordCount++
	store.lastWriteError = ""
	store.pruneLocked(observedAt.Add(-historyRetentionWindow))
	return nil
}

func (store *throughputHistoryStore) appendLegacyBatch(facts []LegacyThroughputFact) error {
	if store == nil || len(facts) == 0 {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	existing := make(map[string]struct{}, len(store.legacy))
	for _, fact := range store.legacy {
		existing[legacyThroughputKey(fact)] = struct{}{}
	}
	records := make([]throughputHistoryRecord, 0, len(facts))
	added := make([]LegacyThroughputFact, 0, len(facts))
	for i := range facts {
		fact, _, ok := normalizeLegacyThroughput(&facts[i])
		if !ok {
			continue
		}
		key := legacyThroughputKey(fact)
		if _, ok := existing[key]; ok {
			continue
		}
		existing[key] = struct{}{}
		copy := fact
		records = append(records, throughputHistoryRecord{SchemaVersion: throughputHistorySchemaVersion, Kind: throughputHistoryKindLegacy, Legacy: &copy})
		added = append(added, fact)
	}
	if len(records) == 0 {
		return nil
	}
	if err := appendThroughputHistoryRecords(store.path, records); err != nil {
		store.lastWriteError = err.Error()
		return err
	}
	store.legacy = append(store.legacy, added...)
	sort.Slice(store.legacy, func(i, j int) bool {
		if store.legacy[i].At == store.legacy[j].At {
			return store.legacy[i].WindowSeconds < store.legacy[j].WindowSeconds
		}
		return store.legacy[i].At < store.legacy[j].At
	})
	store.loadedRecordCount += len(added)
	store.lastWriteError = ""
	return nil
}

func (store *throughputHistoryStore) snapshot() ([]ThroughputMinuteFact, []LegacyThroughputFact) {
	if store == nil {
		return nil, nil
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	minutes := make([]ThroughputMinuteFact, len(store.minutes))
	for i := range store.minutes {
		minutes[i] = cloneThroughputMinute(store.minutes[i])
	}
	legacy := make([]LegacyThroughputFact, len(store.legacy))
	for i := range store.legacy {
		legacy[i] = cloneLegacyThroughput(store.legacy[i])
	}
	return minutes, legacy
}

func (store *throughputHistoryStore) snapshotMetadata() *SnapshotThroughputHistory {
	if store == nil {
		return nil
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	return &SnapshotThroughputHistory{
		StorePath:          store.path,
		MinuteFactCount:    len(store.minutes),
		LegacyFactCount:    len(store.legacy),
		DroppedRecordCount: store.droppedRecordCount,
		CorruptRecordCount: store.corruptRecordCount,
		LastWriteError:     store.lastWriteError,
	}
}

func (store *throughputHistoryStore) pruneLocked(cutoff time.Time) {
	first := sort.Search(len(store.minutes), func(i int) bool {
		at, _ := time.Parse(time.RFC3339, store.minutes[i].At)
		return !at.Before(cutoff)
	})
	if first > 0 {
		store.droppedRecordCount += first
		store.minutes = append([]ThroughputMinuteFact(nil), store.minutes[first:]...)
	}
}

func migrateLegacyThroughputHistory(history *localHistoryState, store *throughputHistoryStore) error {
	if history == nil || store == nil || len(history.samples) == 0 {
		return nil
	}
	facts := make([]LegacyThroughputFact, 0)
	for _, sample := range history.samples {
		throughput := sample.OutputTokenThroughput
		if throughput == nil || throughput.WindowSeconds <= 0 {
			continue
		}
		fact := LegacyThroughputFact{
			At:             sample.At,
			State:          throughput.State,
			WindowSeconds:  throughput.WindowSeconds,
			ActiveSessions: throughput.ActiveSessions,
			Projects:       cloneLiveTokenRateProjectSamples(throughput.Projects),
		}
		if throughput.OutputTokensPerSecond != nil {
			rate := *throughput.OutputTokensPerSecond
			fact.OutputTokensPerSecond = &rate
		}
		facts = append(facts, fact)
	}
	if len(facts) == 0 {
		return nil
	}
	for start := 0; start < len(facts); start += throughputLegacyMigrationBatch {
		end := min(len(facts), start+throughputLegacyMigrationBatch)
		if err := store.appendLegacyBatch(facts[start:end]); err != nil {
			return err
		}
	}
	cleaned := append([]HistorySample(nil), history.samples...)
	for i := range cleaned {
		cleaned[i].OutputTokenThroughput = nil
	}
	lock, err := historyfile.Acquire(history.path)
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := rewriteHistorySampleFile(history.path, cleaned); err != nil {
		return err
	}
	history.samples = cleaned
	return nil
}

func appendThroughputHistoryRecords(path string, records []throughputHistoryRecord) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("throughput history path is empty")
	}
	lock, err := historyfile.Acquire(path)
	if err != nil {
		return err
	}
	defer lock.Release()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(file)
	for _, record := range records {
		if err := json.NewEncoder(writer).Encode(record); err != nil {
			_ = file.Close()
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func rewriteThroughputHistoryFile(path string, minutes []ThroughputMinuteFact, legacy []LegacyThroughputFact) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("throughput history path is empty")
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".compact-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	writer := bufio.NewWriter(tmp)
	encoder := json.NewEncoder(writer)
	for i := range minutes {
		minute := cloneThroughputMinute(minutes[i])
		if err := encoder.Encode(throughputHistoryRecord{SchemaVersion: throughputHistorySchemaVersion, Kind: throughputHistoryKindMinute, Minute: &minute}); err != nil {
			return cleanup(err)
		}
	}
	for i := range legacy {
		fact := cloneLegacyThroughput(legacy[i])
		if err := encoder.Encode(throughputHistoryRecord{SchemaVersion: throughputHistorySchemaVersion, Kind: throughputHistoryKindLegacy, Legacy: &fact}); err != nil {
			return cleanup(err)
		}
	}
	if err := writer.Flush(); err != nil {
		return cleanup(err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return cleanup(err)
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return syncDir(dir)
}

func cloneThroughputMinute(minute ThroughputMinuteFact) ThroughputMinuteFact {
	if minute.OutputTokens != nil {
		tokens := *minute.OutputTokens
		minute.OutputTokens = &tokens
	}
	minute.SessionHashes = append([]string(nil), minute.SessionHashes...)
	if minute.Projects != nil {
		minute.Projects = append([]ThroughputMinuteProjectFact{}, minute.Projects...)
	}
	for i := range minute.Projects {
		minute.Projects[i].SessionHashes = append([]string(nil), minute.Projects[i].SessionHashes...)
	}
	return minute
}

func cloneLegacyThroughput(fact LegacyThroughputFact) LegacyThroughputFact {
	if fact.OutputTokensPerSecond != nil {
		rate := *fact.OutputTokensPerSecond
		fact.OutputTokensPerSecond = &rate
	}
	fact.Projects = cloneLiveTokenRateProjectSamples(fact.Projects)
	return fact
}

func sortThroughputHistory(minutes []ThroughputMinuteFact, legacy []LegacyThroughputFact) {
	sort.Slice(minutes, func(i, j int) bool { return minutes[i].At < minutes[j].At })
	sort.Slice(legacy, func(i, j int) bool {
		if legacy[i].At == legacy[j].At {
			return legacy[i].WindowSeconds < legacy[j].WindowSeconds
		}
		return legacy[i].At < legacy[j].At
	})
}

func searchThroughputMinute(minutes []ThroughputMinuteFact, at string) int {
	return sort.Search(len(minutes), func(i int) bool { return minutes[i].At >= at })
}

func legacyThroughputKey(fact LegacyThroughputFact) string {
	return fact.At + "\x00" + strconv.Itoa(fact.WindowSeconds)
}

func throughputSessionHash(session string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(session)))
	return hex.EncodeToString(sum[:12])
}
