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

// TestDiagnosticLossLedgerKeepsIndependentRows pins the P0 ledger contract at
// the view-model boundary. The two transcript rows must stay separate: one is
// an in-scope scan delay and the other is an explicit history boundary.
func TestDiagnosticLossLedgerKeepsIndependentRows(t *testing.T) {
	model := readUISource(t, diagnosticModelPath)
	for _, key := range []string{
		"unmapped_pid",
		"low_confidence_sessions",
		"deferred_transcript_scan",
		"token_coverage",
		"evidence_walk_cost",
		"evidence_out_of_horizon",
	} {
		if !strings.Contains(model, `"`+key+`"`) {
			t.Errorf("loss ledger row %q is not projected by %s", key, diagnosticModelPath)
		}
	}
	for _, state := range []string{"measured", "partial", "unavailable", "out_of_scope"} {
		if !strings.Contains(model, `"`+state+`"`) {
			t.Errorf("loss ledger state %q is not represented by %s", state, diagnosticModelPath)
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

// TestEveryDiagnosticSignalSourceHasLocalizedCopy is the source-side twin of the
// kind gate above. diagnosticSourceLabel looks up diagnosticSource<Source>Label
// and falls back to humanizing the raw identifier, so a source with no copy
// prints "process observer" in the Japanese and Chinese pages too -- quietly,
// because the fallback always produces something that looks like a word.
func TestEveryDiagnosticSignalSourceHasLocalizedCopy(t *testing.T) {
	copyKeys := i18nKeysPerLocale(t)
	for _, source := range diagnosticSignalSources(t) {
		key := "diagnosticSource" + normalizeDiagnosticKindForI18n(source) + "Label"
		for locale, keys := range copyKeys {
			if !keys[key] {
				t.Errorf("signal source %q has no label copy in locale %d (want key %q in %s)",
					source, locale, key, i18nPath)
			}
		}
	}
}

// TestEveryRiskSignalKindCarriesAMetricFamily gates the join that the evolution
// cards rest on.
//
// buildDiagnosticEvolutionInsights and buildDiagnosticAnomalySignals both write
// a metric_key, and buildEvolutionRows pairs an insight with a signal by
// comparing the two for equality. diagnosticMetricForRisk is the only thing
// standing between a risk kind and that join, and it was a switch whose default
// returned "" -- so a kind that was renamed at the emission site (observer.go
// emits duplicate_overlap_candidates; the switch still read duplicate_overlap)
// silently lost its family, and every signal built from it became unjoinable
// and unlabelled. Nothing failed: "" is a legal value for an omitempty field.
//
// Both lists are scanned from source. A hand-kept list here would need the same
// edit as the rename it exists to catch.
func TestEveryRiskSignalKindCarriesAMetricFamily(t *testing.T) {
	families := map[string]bool{}
	for _, entry := range defaultMetricRegistry() {
		families[entry.Key] = true
	}
	for _, kind := range riskSignalKinds(t) {
		metric := diagnosticMetricForRisk(kind)
		if metric == "" {
			t.Errorf("risk kind %q maps to no metric family.\n"+
				"diagnosticMetricForRisk must name one, or the signal reaches the panel with an\n"+
				"empty metric_key: it cannot join an evolution insight and its evidence column\n"+
				"falls back to a humanized identifier.", kind)
			continue
		}
		if !families[metric] {
			t.Errorf("risk kind %q maps to metric family %q, which defaultMetricRegistry does not define.\n"+
				"A family outside the registry has no semantic definition to be read against.", kind, metric)
		}
	}
}

// TestEveryRiskSignalKindHasAnExportTitle is the same gate for the other switch
// keyed on the same kinds.
//
// Fixing diagnosticMetricForRisk's dead duplicate_overlap branch did not fix
// diagnosticTitleForRisk, which switched on the same stale spelling -- one
// rename, two switches, and only one of them was checked. The panel hides this
// because it re-derives copy from i18n, but /api/diagnostic-export ships the
// Go-side title verbatim and has no second source, so six kinds were shipping
// their own identifier with the underscores taken out
// ("duplicate overlap candidates").
//
// The lesson generalizes past these two functions: when a gate covers one
// consumer of a vocabulary, ask what else switches on it.
func TestEveryRiskSignalKindHasAnExportTitle(t *testing.T) {
	for _, kind := range riskSignalKinds(t) {
		title := diagnosticTitleForRisk(kind)
		// The default arm only strips underscores, so a kind with no branch
		// returns its own identifier with spaces in it.
		if title == strings.ReplaceAll(kind, "_", " ") {
			t.Errorf("risk kind %q has no export title; it ships as %q.\n"+
				"The panel localizes its own copy, but /api/diagnostic-export carries this\n"+
				"string to a reader with nothing else to fall back on.", kind, title)
		}
	}
}

// TestRiskSwitchesNameNoKindNobodyEmits catches the other half: a branch for a
// kind that no longer exists. Such a branch is silent -- it simply never runs,
// and the kind that replaced it falls to the default arm.
func TestRiskSwitchesNameNoKindNobodyEmits(t *testing.T) {
	emitted := map[string]bool{}
	for _, kind := range riskSignalKinds(t) {
		emitted[kind] = true
	}
	source := readUISource(t, "diagnostics.go")
	for _, fn := range []string{"diagnosticTitleForRisk", "diagnosticMetricForRisk"} {
		for _, kind := range switchCaseKinds(t, source, fn) {
			if !emitted[kind] {
				t.Errorf("%s has a case for %q, which observer.go never emits.\n"+
					"The branch is dead and the kind that replaced it falls through to the default.",
					fn, kind)
			}
		}
	}
}

// switchCaseKinds pulls the case literals out of one function body, so the gate
// reads the switch rather than a copy of it kept here.
func switchCaseKinds(t *testing.T, source, fn string) []string {
	t.Helper()
	body := regexp.MustCompile(`(?s)func ` + fn + `\(kind string\) string \{.*?\n\}`).FindString(source)
	if body == "" {
		t.Fatalf("could not find %s in diagnostics.go -- the scan pattern has drifted", fn)
	}
	kinds := []string{}
	// Only the case labels: a bare string scan also picks up every return value
	// and the ReplaceAll arguments in the default arm.
	for _, line := range regexp.MustCompile(`(?m)^\s*case\s+(.+):$`).FindAllStringSubmatch(body, -1) {
		for _, match := range regexp.MustCompile(`"([a-z0-9_]+)"`).FindAllStringSubmatch(line[1], -1) {
			kinds = append(kinds, match[1])
		}
	}
	if len(kinds) < 4 {
		t.Fatalf("found only %d case literals in %s -- the scan pattern has drifted", len(kinds), fn)
	}
	return kinds
}

// riskSignalKinds scans the kinds observer.go actually emits into
// CoordinationRisk.Signals. It deliberately reads only observer.go: those are
// the kinds that flow through diagnosticMetricForRisk.
func riskSignalKinds(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("observer.go")
	if err != nil {
		t.Fatalf("read observer.go: %v", err)
	}
	pattern := regexp.MustCompile(`RiskSignalSnapshot\{\s*Kind:\s*"([a-z0-9_]+)"`)
	matches := pattern.FindAllStringSubmatch(string(raw), -1)
	if len(matches) < 5 {
		t.Fatalf("found only %d risk signal kinds in observer.go -- the scan pattern has drifted", len(matches))
	}
	kinds := make([]string, 0, len(matches))
	for _, match := range matches {
		kinds = append(kinds, match[1])
	}
	return kinds
}

// diagnosticSignalSources scans the Source values diagnostics.go attaches to
// signals, for the same reason diagnosticSignalKinds scans kinds.
func diagnosticSignalSources(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("diagnostics.go")
	if err != nil {
		t.Fatalf("read diagnostics.go: %v", err)
	}
	pattern := regexp.MustCompile(`Source:\s*"([a-z0-9_]+)"`)
	seen := map[string]bool{}
	sources := []string{}
	for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
		if seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		sources = append(sources, match[1])
	}
	if len(sources) < 5 {
		t.Fatalf("found only %d signal sources in diagnostics.go -- the scan pattern has drifted", len(sources))
	}
	return sources
}

// TestEvolutionInsightsNameSignalsThatExist pins the provenance join.
//
// An evolution card ends with "Traced to: <signals>", which is a claim about
// where its numbers came from. The join was metric_key equality, and a metric
// key is a semantic family shared by several unrelated signals -- so the card
// built from stale-session and recent-session counts traced itself to the
// low-confidence and workitem-coverage rows, naming evidence it had never read.
// A wrong provenance line is worse than none: it invites the reader to check a
// number against a row that cannot confirm it.
//
// The fix was to have each insight name its own SignalKinds. This gate keeps
// those names honest: a kind nobody emits produces no trace line at all, which
// is the same silent nothing the metric_key join produced.
func TestEvolutionInsightsNameSignalsThatExist(t *testing.T) {
	emitted := map[string]bool{}
	for _, kind := range diagnosticSignalKinds(t) {
		emitted[kind] = true
	}
	// A snapshot that trips every insight branch at once, so all of them are
	// built and checked rather than only the ones a zero value happens to fire.
	snap := snapshot.Snapshot{
		Summary: snapshot.SnapshotSummary{UnmappedProcesses: 3},
		CoordinationRisk: snapshot.CoordinationRiskSnapshot{
			StaleSessionCount:              2,
			ChurnSessionCount:              1,
			DuplicateOverlapSuspicionCount: 2,
			ProjectSpreadCount:             4,
			LowConfidenceSessionCount:      5,
		},
	}
	insights := buildDiagnosticEvolutionInsights(snap)
	if len(insights) < 3 {
		t.Fatalf("expected the triggering snapshot to build every insight, got %d", len(insights))
	}
	for _, insight := range insights {
		for _, kind := range insight.SignalKinds {
			if !emitted[kind] {
				t.Errorf("evolution insight %q names signal kind %q, which nothing emits.\n"+
					"The card's \"traced to\" line would silently render empty -- a provenance\n"+
					"claim that points at nothing.", insight.Key, kind)
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
