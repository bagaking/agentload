package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"agentload/internal/historyfile"
)

// Revisions are an append journal, not a second history. Check admission at
// most once a minute; roll cold rows hourly or fold an 8 MiB hot journal.
const throughputHotJournalBytes = 8 * 1024 * 1024

type throughputStoredRecord struct {
	record throughputHistoryRecord
	at     time.Time
	line   []byte
}

func parseThroughputStoredRecord(line []byte) (throughputStoredRecord, error) {
	var row throughputStoredRecord
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&row.record); err != nil {
		return row, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return row, errors.New("invalid throughput record tail")
	}
	if row.record.SchemaVersion != throughputHistorySchemaVersion {
		return row, errors.New("unknown throughput schema; preserved")
	}
	var ok bool
	switch row.record.Kind {
	case throughputHistoryKindMinute:
		if row.record.Legacy != nil {
			return row, errors.New("mixed throughput families; preserved")
		}
		_, row.at, ok = normalizeThroughputMinute(row.record.Minute)
	case throughputHistoryKindLegacy:
		if row.record.Minute != nil {
			return row, errors.New("mixed throughput families; preserved")
		}
		_, row.at, ok = normalizeLegacyThroughput(row.record.Legacy)
	default:
		return row, errors.New("unknown throughput kind; preserved")
	}
	if !ok {
		return row, errors.New("invalid throughput fact; preserved")
	}
	row.line = append([]byte(nil), line...)
	return row, nil
}

func (r throughputStoredRecord) key() string {
	if r.record.Minute != nil {
		return "minute:" + r.at.UTC().Format(time.RFC3339Nano)
	}
	return "legacy:" + r.at.UTC().Format(time.RFC3339Nano) + fmt.Sprintf(":%d", r.record.Legacy.WindowSeconds)
}

func shouldReplaceThroughputMinute(old, next ThroughputMinuteFact) bool {
	return old.Origin == "session_replay" || old.OutputTokens == nil || next.Origin != "session_replay"
}

func foldThroughputRecord(rows map[string]throughputStoredRecord, row throughputStoredRecord) {
	key := row.key()
	if old, exists := rows[key]; exists && old.record.Minute != nil {
		// Numeric online evidence is authoritative even if replay arrives later.
		if !shouldReplaceThroughputMinute(*old.record.Minute, *row.record.Minute) {
			return
		}
	}
	rows[key] = row
}

func scanThroughputRecords(reader io.Reader, rows map[string]throughputStoredRecord) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		row, err := parseThroughputStoredRecord(line)
		if err != nil {
			return err
		}
		foldThroughputRecord(rows, row)
	}
	return scanner.Err()
}

func sortedThroughputLines(rows map[string]throughputStoredRecord) [][]byte {
	keys := make([]string, 0, len(rows))
	for key := range rows {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := rows[keys[i]], rows[keys[j]]
		if a.at.Equal(b.at) {
			return keys[i] < keys[j]
		}
		return a.at.Before(b.at)
	})
	lines := make([][]byte, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, rows[key].line)
	}
	return lines
}

// Same lock as append. Read the durable journal, rather than only the retained
// in-memory view: an unsuccessful append may have committed a valid prefix.
// Unknown/corrupt data prevents replacement, including at loader startup.
func maintainThroughputHistoryFile(path string, now time.Time, checkCapacity func(string, uint64) error) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != 0 {
		tail := make([]byte, 1)
		if _, err = file.ReadAt(tail, info.Size()-1); err != nil {
			return err
		}
		if tail[0] != '\n' {
			return errors.New("incomplete throughput journal tail; preserved")
		}
	}
	rows := map[string]throughputStoredRecord{}
	if err := scanThroughputRecords(file, rows); err != nil {
		return err
	}
	hot := map[string]throughputStoredRecord{}
	cold := map[string]map[string]throughputStoredRecord{}
	for key, row := range rows {
		if !row.at.Before(now.Add(-historyHotWindow)) {
			hot[key] = row
			continue
		}
		month := archiveMonth(row.at)
		if cold[month] == nil {
			cold[month] = map[string]throughputStoredRecord{}
		}
		cold[month][key] = row
	}
	months := make([]string, 0, len(cold))
	for month := range cold {
		months = append(months, month)
	}
	sort.Strings(months)
	for _, month := range months {
		archive := archivePartitionPath(path, month)
		merged := map[string]throughputStoredRecord{}
		if err := scanArchivePartition(archive, func(reader io.Reader) error { return scanThroughputRecords(reader, merged) }); err != nil {
			return err
		}
		for _, row := range cold[month] {
			foldThroughputRecord(merged, row)
		}
		lines := sortedThroughputLines(merged)
		// Incompressible output is bounded by input plus gzip overhead. Reserve
		// the current temporary partition, not another copy of the entire store.
		var peak uint64 = 64 * 1024
		for _, line := range lines {
			peak += uint64(len(line) + 1)
		}
		if err := checkCapacity(path, peak); err != nil {
			return err
		}
		if err := replaceThroughputLines(archive, lines, true); err != nil {
			return err
		}
	}
	lines := sortedThroughputLines(hot)
	var peak uint64
	for _, line := range lines {
		peak += uint64(len(line) + 1)
	}
	if err := checkCapacity(path, peak); err != nil {
		return err
	}
	// Archives are durable before replacing the hot journal. Retrying folds
	// duplicates by stable key and cannot add the same tokens twice.
	return replaceThroughputLines(path, lines, false)
}

func replaceThroughputLines(path string, lines [][]byte, compressed bool) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".compact-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	var writer io.Writer = tmp
	var zip *gzip.Writer
	if compressed {
		zip = gzip.NewWriter(tmp)
		writer = zip
	}
	buffer := bufio.NewWriter(writer)
	for _, line := range lines {
		if _, err = buffer.Write(line); err != nil {
			return err
		}
		if err = buffer.WriteByte('\n'); err != nil {
			return err
		}
	}
	if err = buffer.Flush(); err != nil {
		return err
	}
	if zip != nil {
		if err = zip.Close(); err != nil {
			return err
		}
	}
	if err = tmp.Chmod(0644); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

// Caller holds the store mutex. This runs only after a successful durable
// append; a maintenance error is observable but cannot undo that append.
func (store *throughputHistoryStore) maintainLocked(now time.Time) {
	if now.Before(store.nextMaintenance) {
		return
	}
	store.nextMaintenance = now.Add(time.Minute)
	info, err := os.Stat(store.path)
	if err == nil && info.Size() < throughputHotJournalBytes && now.Before(store.nextColdRoll) {
		return
	}
	if err == nil {
		lock, e := historyfile.Acquire(store.path)
		if e != nil {
			err = e
		} else {
			err = maintainThroughputHistoryFile(store.path, now, historyfile.CheckStorageCapacity)
			lock.Release()
		}
	}
	if err != nil {
		store.lastWriteError = err.Error()
		return
	}
	store.nextColdRoll = now.Add(time.Hour)
}
