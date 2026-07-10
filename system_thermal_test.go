package main

import "testing"

func TestNormalizeThermalState(t *testing.T) {
	tests := []struct {
		value int
		want  string
		ok    bool
	}{
		{value: 0, want: "nominal", ok: true},
		{value: 1, want: "fair", ok: true},
		{value: 2, want: "serious", ok: true},
		{value: 3, want: "critical", ok: true},
		{value: -1, want: "", ok: false},
		{value: 9, want: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := normalizeThermalState(tt.value)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("normalizeThermalState(%d) = (%q, %t), want (%q, %t)", tt.value, got, ok, tt.want, tt.ok)
		}
	}
}
