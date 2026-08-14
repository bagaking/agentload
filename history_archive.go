package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// historyHotWindow is how much recent history each JSONL store keeps as plain
// text. Older rows move into per-month gzip partitions beside the hot file.
//
// The hot file keeps its exact name, format, and per-row fsync append path, so
// compression never weakens the "a crash loses at most one row" guarantee.
const historyHotWindow = 2 * 24 * time.Hour

// archivePartitionPath names the gzip partition holding a month of cold rows,
// e.g. history.jsonl -> history.2026-08.jsonl.gz. Partitions are selected by
// filename, so a reader covering the retention window never opens older ones.
func archivePartitionPath(dataPath, month string) string {
	dataPath = strings.TrimSpace(dataPath)
	if dataPath == "" || month == "" {
		return ""
	}
	base := filepath.Base(dataPath)
	stem := strings.TrimSuffix(base, ".jsonl")
	return filepath.Join(filepath.Dir(dataPath), fmt.Sprintf("%s.%s.jsonl.gz", stem, month))
}

// archiveMonth extracts the YYYY-MM partition key from an RFC3339 timestamp.
func archiveMonth(at time.Time) string {
	return at.UTC().Format("2006-01")
}

// archivePartitionsSince lists existing partitions that can still hold rows at
// or after cutoff. Older partitions stay on disk untouched and unread.
func archivePartitionsSince(dataPath string, cutoff time.Time) ([]string, error) {
	dataPath = strings.TrimSpace(dataPath)
	if dataPath == "" {
		return nil, nil
	}
	// A partition is in range when its month has not fully ended before cutoff,
	// so compare against the first instant of the cutoff month.
	oldest := archiveMonth(cutoff)
	base := strings.TrimSuffix(filepath.Base(dataPath), ".jsonl")
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(dataPath), base+".*.jsonl.gz"))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		month := archiveMonthFromPath(base, match)
		if month == "" || month < oldest {
			continue
		}
		out = append(out, match)
	}
	sort.Strings(out)
	return out, nil
}

func archiveMonthFromPath(base, path string) string {
	name := filepath.Base(path)
	name = strings.TrimPrefix(name, base+".")
	name = strings.TrimSuffix(name, ".jsonl.gz")
	if len(name) != len("2006-01") {
		return ""
	}
	return name
}

// readArchivePartition returns every line the partition holds. A partition that
// does not exist yields no lines and no error; a partition that exists but
// cannot be fully read returns an error so the caller can refuse to act on a
// partial view instead of silently treating missing history as empty.
func readArchivePartition(path string) ([][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("read archive %s: %w", filepath.Base(path), err)
	}
	defer reader.Close()
	reader.Multistream(true)

	lines := [][]byte{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		lines = append(lines, append([]byte(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read archive %s: %w", filepath.Base(path), err)
	}
	return lines, nil
}

// readArchiveLines concatenates several partitions in the order given.
func readArchiveLines(paths []string) ([][]byte, error) {
	out := [][]byte{}
	for _, path := range paths {
		lines, err := readArchivePartition(path)
		if err != nil {
			return nil, err
		}
		out = append(out, lines...)
	}
	return out, nil
}

// writeArchivePartition merges lines into the partition and rewrites it whole.
//
// The whole-partition rewrite is deliberate. Appending a second gzip member is
// cheaper, but a member truncated by a crash makes every member after it
// unreadable, so recovery would need its own tail-repair mechanism. Rewriting
// is idempotent -- the result is always "existing partition + new rows", with
// rows the partition already holds dropped -- so re-archiving the same cold row
// (every compaction re-offers it, since the reader merges archive and hot rows
// into one set) cannot make the partition grow without bound.
func writeArchivePartition(path string, lines [][]byte) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("archive partition path is empty")
	}
	if len(lines) == 0 {
		return nil
	}
	existing, err := readArchivePartition(path)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(existing))
	for _, line := range existing {
		seen[string(line)] = struct{}{}
	}
	fresh := make([][]byte, 0, len(lines))
	for _, line := range lines {
		if _, ok := seen[string(line)]; ok {
			continue
		}
		seen[string(line)] = struct{}{}
		fresh = append(fresh, line)
	}
	if len(fresh) == 0 {
		return nil
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

	writer := gzip.NewWriter(tmp)
	for _, group := range [][][]byte{existing, fresh} {
		for _, line := range group {
			if _, err := writer.Write(append(line, '\n')); err != nil {
				return cleanup(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
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
	// Rename is atomic but not durable. Without this the archive rename can be
	// lost while the hot-file truncation survives, which would be silent loss
	// rather than the recoverable duplicate the ordering is designed for.
	return syncDir(dir)
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return err
	}
	return handle.Close()
}

// splitColdRowsByMonth groups cold rows into their partition months, preserving
// order within each month.
func splitColdRowsByMonth(rows []archiveRow) map[string][][]byte {
	out := map[string][][]byte{}
	for _, row := range rows {
		month := archiveMonth(row.At)
		out[month] = append(out[month], row.Line)
	}
	return out
}

// archiveRow is one cold line together with the timestamp that decides which
// month partition it belongs to.
type archiveRow struct {
	At   time.Time
	Line []byte
}

// archiveColdRows writes cold rows into their month partitions. It returns
// after every partition is durably in place, so the caller may then rewrite the
// hot file: a crash in between leaves the rows in both places (a duplicate the
// readers absorb) rather than in neither.
func archiveColdRows(dataPath string, rows []archiveRow) error {
	if len(rows) == 0 {
		return nil
	}
	byMonth := splitColdRowsByMonth(rows)
	months := make([]string, 0, len(byMonth))
	for month := range byMonth {
		months = append(months, month)
	}
	sort.Strings(months)
	for _, month := range months {
		if err := writeArchivePartition(archivePartitionPath(dataPath, month), byMonth[month]); err != nil {
			return err
		}
	}
	return nil
}
