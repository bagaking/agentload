package trajectory

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFactBlockBatchCachePreservesEvidenceBoundaries(t *testing.T) {
	cache := &factBlockReadCache{}
	ctx := context.Background()
	text := strings.Repeat("recorded source evidence ", 1000)
	body, err := encodeTextStored(text)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, err := cache.slice(ctx, body, 4, 19)
		if err != nil || string(got) != text[4:23] {
			t.Fatalf("read %d: %q %v", i, got, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := cache.slice(cancelled, body, 4, 19); !errors.Is(err, context.Canceled) {
		t.Fatalf("cached cancellation: %v", err)
	}
	if _, err := cache.slice(ctx, body, len(text)-1, 2); err == nil {
		t.Fatal("cached invalid extent accepted")
	}
	replacement, err := encodeTextStored(strings.Repeat("other recorded evidence ", 1000))
	if err != nil {
		t.Fatal(err)
	}
	got, err := cache.slice(ctx, replacement, 0, 5)
	if err != nil || string(got) != "other" {
		t.Fatalf("replacement used old bytes: %q %v", got, err)
	}
	if body[3] != 4 {
		t.Fatal("expected compressed checksum fixture")
	}
	corrupt := bytes.Clone(body)
	corrupt[len(corrupt)-1] ^= 1
	if _, err := cache.slice(ctx, corrupt, 0, 5); err == nil {
		t.Fatal("corrupt replacement bypassed decoding")
	}
}

func TestFactBlockBatchCacheBoundsAndOversizedBypass(t *testing.T) {
	ctx := context.Background()
	cache := &factBlockReadCache{}
	large := strings.Repeat("large record ", factBlockBytes/4)
	body, err := encodeTextStored(large)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, err := cache.slice(ctx, body, len(large)-8, 8)
		if err != nil || string(got) != large[len(large)-8:] {
			t.Fatalf("large record %v", err)
		}
	}
	for _, entry := range cache.entries {
		if entry.body != nil {
			t.Fatal("oversized block retained")
		}
	}
	for i := 0; i < 9; i++ {
		text := strings.Repeat(string(rune('a'+i)), factBlockBytes)
		body, err := encodeTextStored(text)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := cache.slice(ctx, body, 0, 1); err != nil || string(got) != text[:1] {
			t.Fatal(err)
		}
	}
	retained := 0
	for _, entry := range cache.entries {
		if len(entry.body) > factBlockBytes+64 || len(entry.raw) > factBlockBytes {
			t.Fatal("individual cache limit exceeded")
		}
		retained += len(entry.body) + len(entry.raw)
	}
	if retained > 4*(2*factBlockBytes+64) {
		t.Fatal("batch cache total limit exceeded")
	}
}
