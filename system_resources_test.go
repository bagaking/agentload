package main

import "testing"

func TestCounterDelta(t *testing.T) {
	tests := []struct {
		name     string
		previous uint64
		current  uint64
		want     uint64
	}{
		{name: "increase", previous: 10, current: 16, want: 6},
		{name: "same", previous: 10, current: 10, want: 0},
		{name: "reset", previous: 16, current: 10, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := counterDelta(tt.previous, tt.current); got != tt.want {
				t.Fatalf("counterDelta(%d, %d) = %d, want %d", tt.previous, tt.current, got, tt.want)
			}
		})
	}
}
