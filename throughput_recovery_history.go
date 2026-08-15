package main

import (
	"encoding/json"
	"sort"
	"time"
)

// Replay replaces its own earlier projection or a missing online slot. Numeric
// online minutes are authoritative observations and never added to replay: the
// aggregate cannot prove which individual tokens overlap with those old facts.
// The minute key is stable, so crash/retry/restart only replaces, never sums.
func (store *throughputHistoryStore) mergeRecoveredMinutes(facts []ThroughputMinuteFact, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	existing := make(map[string]ThroughputMinuteFact, len(store.minutes))
	for _, fact := range store.minutes {
		existing[fact.At] = fact
	}
	records := make([]throughputHistoryRecord, 0, 256)
	commit := func() error {
		if len(records) == 0 {
			return nil
		}
		if err := appendThroughputHistoryRecords(store.path, records); err != nil {
			store.lastWriteError = err.Error()
			return err
		}
		for _, record := range records {
			existing[record.Minute.At] = *record.Minute
		}
		store.loadedRecordCount += len(records)
		records = nil
		return nil
	}
	var writeErr error
	for _, raw := range facts {
		fact, at, ok := normalizeThroughputMinute(&raw)
		if !ok || fact.Origin != "session_replay" || fact.OutputTokens == nil || *fact.OutputTokens <= 0 || at.Before(now.Add(-historyRetentionWindow)) || at.After(now.Truncate(time.Minute)) {
			continue
		}
		if previous, exists := existing[fact.At]; exists {
			if previous.Origin != "session_replay" && previous.OutputTokens != nil {
				continue
			}
			a, _ := json.Marshal(previous)
			b, _ := json.Marshal(fact)
			if string(a) == string(b) {
				continue
			}
		}
		copy := fact
		records = append(records, throughputHistoryRecord{SchemaVersion: throughputHistorySchemaVersion, Kind: throughputHistoryKindMinute, Minute: &copy})
		if len(records) >= 256 {
			if err := commit(); err != nil {
				writeErr = err
				break
			}
		}
	}
	err := writeErr
	if err == nil {
		err = commit()
	}
	// Reflect committed batches even when a later append fails. The next replay
	// retries only the missing facts; append failure cannot lose in-memory truth.
	store.minutes = store.minutes[:0]
	for _, fact := range existing {
		store.minutes = append(store.minutes, fact)
	}
	sort.Slice(store.minutes, func(i, j int) bool { return store.minutes[i].At < store.minutes[j].At })
	store.pruneLocked(now.Add(-historyRetentionWindow))
	if err == nil {
		store.lastWriteError = ""
	}
	return err
}
