package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"agentload/internal/snapshot"
)

const (
	diagnosticModelPath = "ui/src/diagnostics/diagnosticModel.ts"
	i18nPath            = "ui/src/i18n.ts"
)

// TestEveryDiagnosticBaselineReachesThePanel is the gate for computed-then-
// dropped evidence.
//
// The backend built five baselines and the panel picked three of them by name.
// evidence_walk_cost and low_confidence_sessions were computed on every
// snapshot, serialized into /api/snapshot, and then silently discarded by the
// view model -- so the API said the scan cost was measured while the page
// showed nothing. Nothing failed, because nothing compared the two lists.
//
// The check is textual on purpose: the view model selects baselines by string
// literal, so a string search is the same question the code asks.
func TestEveryDiagnosticBaselineReachesThePanel(t *testing.T) {
	model := readUISource(t, diagnosticModelPath)
	for _, baseline := range buildDiagnosticBaselines(snapshot.Snapshot{}) {
		if !strings.Contains(model, `"`+baseline.Key+`"`) {
			t.Errorf("baseline %q is built by buildDiagnosticBaselines but never named in %s.\n"+
				"Either render it or stop computing it -- an evidence row the page drops is a metric nobody can check.",
				baseline.Key, diagnosticModelPath)
		}
	}
}

// TestEveryDiagnosticSignalKindHasLocalizedCopy pins the other half of the same
// hazard. The UI throws away the Go-side Title/Detail and looks up
// diagnosticSignal<Kind>Title/Detail instead, so a signal kind with no copy
// renders as a humanized raw identifier -- in all three locales at once.
func TestEveryDiagnosticSignalKindHasLocalizedCopy(t *testing.T) {
	copyKeys := i18nKeysPerLocale(t)
	for _, kind := range diagnosticSignalKinds(t) {
		normalized := normalizeDiagnosticKindForI18n(kind)
		for _, suffix := range []string{"Title", "Detail"} {
			key := "diagnosticSignal" + normalized + suffix
			for locale, keys := range copyKeys {
				if !keys[key] {
					t.Errorf("signal kind %q has no %s copy in locale %d (want key %q in %s)",
						kind, suffix, locale, key, i18nPath)
				}
			}
		}
	}
}

// diagnosticSignalKinds reads the kinds straight out of the source rather than
// from a hand-kept list here: a list would need editing by the same change that
// adds a kind, which is exactly the step this test exists to not rely on.
func diagnosticSignalKinds(t *testing.T) []string {
	t.Helper()
	sources := []string{"diagnostics.go", "observer.go"}
	pattern := regexp.MustCompile(`Kind:\s*"([a-z0-9_]+)"`)
	seen := map[string]bool{}
	kinds := []string{}
	for _, path := range sources {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
			if seen[match[1]] {
				continue
			}
			seen[match[1]] = true
			kinds = append(kinds, match[1])
		}
	}
	if len(kinds) < 20 {
		t.Fatalf("found only %d signal kinds across %v -- the scan pattern has drifted", len(kinds), sources)
	}
	return kinds
}

// i18nKeysPerLocale returns one key set per locale block. Locales are counted
// by brace-delimited blocks rather than by name so that adding a fourth
// language is covered without touching this test.
func i18nKeysPerLocale(t *testing.T) map[int]map[string]bool {
	t.Helper()
	source := readUISource(t, i18nPath)
	keyPattern := regexp.MustCompile(`(?m)^    ([A-Za-z0-9_]+):`)
	localePattern := regexp.MustCompile(`(?m)^  [a-z]{2}: \{`)
	starts := localePattern.FindAllStringIndex(source, -1)
	if len(starts) < 3 {
		t.Fatalf("found %d locale blocks in %s, want at least 3", len(starts), i18nPath)
	}
	locales := map[int]map[string]bool{}
	for i, start := range starts {
		end := len(source)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		keys := map[string]bool{}
		for _, match := range keyPattern.FindAllStringSubmatch(source[start[0]:end], -1) {
			keys[match[1]] = true
		}
		locales[i] = keys
	}
	return locales
}

// normalizeDiagnosticKindForI18n mirrors normalizeI18nKey in diagnosticModel.ts:
// split on non-alphanumerics, capitalize each part, join.
func normalizeDiagnosticKindForI18n(kind string) string {
	parts := strings.FieldsFunc(kind, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	})
	out := strings.Builder{}
	for _, part := range parts {
		out.WriteString(strings.ToUpper(part[:1]))
		out.WriteString(part[1:])
	}
	return out.String()
}

func readUISource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
