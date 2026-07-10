package main

import (
	"context"
	"testing"
	"time"
)

func TestClampDuration(t *testing.T) {
	tests := []struct {
		name    string
		value   time.Duration
		floor   time.Duration
		ceiling time.Duration
		want    time.Duration
	}{
		{name: "below floor uses floor", value: 10 * time.Second, floor: 45 * time.Second, ceiling: 5 * time.Minute, want: 45 * time.Second},
		{name: "zero value uses floor", value: 0, floor: 90 * time.Second, ceiling: 5 * time.Minute, want: 90 * time.Second},
		{name: "in range passes through", value: 2 * time.Minute, floor: 45 * time.Second, ceiling: 5 * time.Minute, want: 2 * time.Minute},
		{name: "above ceiling uses ceiling", value: 30 * time.Minute, floor: 45 * time.Second, ceiling: 5 * time.Minute, want: 5 * time.Minute},
		{name: "default lookback tenth is capped", value: 7 * 24 * time.Hour / 10, floor: 45 * time.Second, ceiling: 5 * time.Minute, want: 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampDuration(tt.value, tt.floor, tt.ceiling); got != tt.want {
				t.Fatalf("clampDuration(%s, %s, %s) = %s, want %s", tt.value, tt.floor, tt.ceiling, got, tt.want)
			}
		})
	}
}

func TestSnapshotScanAborted(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name     string
		ctx      context.Context
		snapshot Snapshot
		want     bool
	}{
		{name: "live context and clean scan", ctx: context.Background(), snapshot: Snapshot{}, want: false},
		{name: "cancelled context", ctx: cancelled, snapshot: Snapshot{}, want: true},
		{
			name:     "scan aborted early marker",
			ctx:      context.Background(),
			snapshot: Snapshot{TranscriptStats: TranscriptStats{Errors: []string{"transcript scan aborted early (3 files not parsed): context deadline exceeded"}}},
			want:     true,
		},
		{
			name:     "scan wait cancelled marker",
			ctx:      context.Background(),
			snapshot: Snapshot{TranscriptStats: TranscriptStats{Errors: []string{"transcript scan wait cancelled: context canceled"}}},
			want:     true,
		},
		{
			name:     "ordinary parse error is not an abort",
			ctx:      context.Background(),
			snapshot: Snapshot{TranscriptStats: TranscriptStats{Errors: []string{"/tmp/x.jsonl: invalid JSON"}}},
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := snapshotScanAborted(tt.ctx, tt.snapshot); got != tt.want {
				t.Fatalf("snapshotScanAborted() = %v, want %v", got, tt.want)
			}
		})
	}
}
