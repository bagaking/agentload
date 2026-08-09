package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const liveTokenRateRetiredEventThreshold = 32_768

func BenchmarkCodingAgentUsageDecoder(b *testing.B) {
	registry := defaultCodingAgentRegistry(Config{})
	lines := map[string][]byte{
		"claude": []byte(`{"timestamp":"2026-08-10T12:00:30Z","sessionId":"session-a","message":{"id":"message-000001","usage":{"input_tokens":8,"output_tokens":1}}}`),
		"codex":  []byte(`{"timestamp":"2026-08-10T12:00:30Z","payload":{"info":{"total_token_usage":{"input_tokens":100,"output_tokens":1}}}}`),
		"trae":   []byte(`{"timestamp":"2026-08-10T12:00:30Z","payload":{"info":{"total_token_usage":{"input_tokens":100,"output_tokens":1}}}}`),
	}
	for _, agent := range []string{"claude", "codex", "trae"} {
		decoder, _ := registry.usageDecoder(agent)
		line := lines[agent]
		b.Run(agent, func(b *testing.B) {
			b.ReportAllocs()
			for iteration := 0; iteration < b.N; iteration++ {
				if _, ok := decoder.DecodeUsage(line); !ok {
					b.Fatal("usage line was not decoded")
				}
			}
		})
	}
}

// One operation ingests the retired raw-event threshold across Claude, Codex,
// and Trae files. A 1000x benchtime therefore proves 32,768,000 append updates.
func BenchmarkLiveTokenRateMultiFileAppend(b *testing.B) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	appendAt := now.Add(30 * time.Second)
	root := b.TempDir()
	claudeRoot := filepath.Join(root, "claude")
	codexRoot := filepath.Join(root, "codex")
	traeRoot := filepath.Join(root, "trae")
	paths := []string{
		filepath.Join(claudeRoot, "projects", "project-a", "session.jsonl"),
		filepath.Join(codexRoot, "sessions", "session.jsonl"),
		filepath.Join(traeRoot, "sessions", "2026", "08", "10", "session.jsonl"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			b.Fatal(err)
		}
	}

	counts := []int{
		liveTokenRateRetiredEventThreshold / 3,
		liveTokenRateRetiredEventThreshold / 3,
		liveTokenRateRetiredEventThreshold - 2*(liveTokenRateRetiredEventThreshold/3),
	}
	appends := [][]byte{
		benchmarkClaudeUsageLines(appendAt, counts[0]),
		benchmarkCumulativeUsageLines(appendAt, counts[1]),
		benchmarkCumulativeUsageLines(appendAt, counts[2]),
	}
	baselines := [][]byte{
		[]byte(fmt.Sprintf(`{"timestamp":%q,"sessionId":"session-a","message":{"id":"baseline","usage":{"output_tokens":0}}}`+"\n", now.Format(time.RFC3339Nano))),
		[]byte(cumulativeTokenLine(now, 0)),
		[]byte(cumulativeTokenLine(now, 0)),
	}
	tools := []string{"claude", "codex", "trae"}
	cfg := Config{ClaudeRoots: []string{claudeRoot}, CodexRoots: []string{codexRoot}, TraeRoots: []string{traeRoot}}
	registry := defaultCodingAgentRegistry(cfg)
	sampler := newLiveTokenRateSampler(registry, newTranscriptEvidenceIndex(registry))
	totalBytes := 0
	for _, appendData := range appends {
		totalBytes += len(appendData)
	}
	b.SetBytes(int64(totalBytes))
	b.ReportAllocs()
	b.ReportMetric(liveTokenRateRetiredEventThreshold, "updates/op")
	b.ResetTimer()

	for iteration := 0; iteration < b.N; iteration++ {
		b.StopTimer()
		tracked := make([]liveTokenRateTrackedFile, len(paths))
		for index, path := range paths {
			if err := os.WriteFile(path, baselines[index], 0o600); err != nil {
				b.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			tracked[index] = sampler.rebaselineFile(path, tools[index], info, now)
		}
		b.StartTimer()

		allBuckets := make([]liveTokenRateEvent, 0, len(paths))
		for index, path := range paths {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := file.Write(appends[index]); err != nil {
				_ = file.Close()
				b.Fatal(err)
			}
			if err := file.Close(); err != nil {
				b.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			decoder, ok := registry.usageDecoder(tools[index])
			if !ok {
				b.Fatalf("missing %s usage decoder", tools[index])
			}
			updated, buckets, _, _ := liveTokenRateReadAppend(path, tracked[index], info, appendAt, decoder)
			tracked[index] = updated
			allBuckets = append(allBuckets, buckets...)
		}
		b.StopTimer()

		tokens, _ := liveTokenRateWindowFacts(allBuckets, appendAt, liveTokenRateWindow, liveTokenRateFutureSkew)
		if tokens != liveTokenRateRetiredEventThreshold {
			b.Fatalf("ingested tokens = %d, want %d", tokens, liveTokenRateRetiredEventThreshold)
		}
		if len(tracked[0].MessageUsage) > liveTokenRateMaxMessages || tracked[0].MessageOrder.Len() > liveTokenRateMaxMessages {
			b.Fatalf("Claude dedupe exceeded bound: map=%d order=%d", len(tracked[0].MessageUsage), tracked[0].MessageOrder.Len())
		}
	}
}

func benchmarkClaudeUsageLines(at time.Time, count int) []byte {
	var lines strings.Builder
	lines.Grow(count * 150)
	for index := 0; index < count; index++ {
		fmt.Fprintf(
			&lines,
			`{"timestamp":%q,"sessionId":"session-a","message":{"id":"message-%06d","usage":{"input_tokens":8,"output_tokens":1}}}`+"\n",
			at.Format(time.RFC3339Nano),
			index,
		)
	}
	return []byte(lines.String())
}

func benchmarkCumulativeUsageLines(at time.Time, count int) []byte {
	var lines strings.Builder
	lines.Grow(count * 150)
	for output := 1; output <= count; output++ {
		fmt.Fprintf(
			&lines,
			`{"timestamp":%q,"payload":{"info":{"total_token_usage":{"input_tokens":100,"output_tokens":%d}}}}`+"\n",
			at.Format(time.RFC3339Nano),
			output,
		)
	}
	return []byte(lines.String())
}
