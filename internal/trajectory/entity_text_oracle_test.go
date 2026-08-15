package trajectory

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// Frozen pre-optimization text scanner. The oracle retains the complete
// occurrence ordering, path/file overlap and truncation behavior.
func originalEntityText(x *entityExtractor, text, nativeField string) {
	if len(text) > 16384 {
		text = text[:16384]
		x.omission("entity_text_scan_limit")
	}
	paths := entityPathPattern.FindAllStringIndex(text, maxEntityOccurrences+1)
	for _, p := range paths {
		start := strings.LastIndexAny(text[:p[0]], " \n\t\r<>\"'`") + 1
		if strings.Contains(text[start:p[0]], ":") {
			continue
		}
		x.pathOccurrence(text[p[0]:p[1]], "mention", nativeField)
	}
	for _, p := range entityFilePattern.FindAllStringIndex(text, maxEntityOccurrences+1) {
		within := false
		for _, path := range paths {
			if p[0] >= path[0] && p[1] <= path[1] {
				within = true
				break
			}
		}
		if !within {
			x.pathOccurrence(text[p[0]:p[1]], "mention", nativeField)
		}
	}
	for _, m := range entitySkillPattern.FindAllStringSubmatch(text, maxEntityOccurrences+1) {
		x.add("skill", m[1], "mention", nativeField)
	}
	for _, m := range entityToolPattern.FindAllStringSubmatch(text, maxEntityOccurrences+1) {
		x.add("tool", m[1], "mention", nativeField)
	}
	for _, m := range entityVersionPattern.FindAllString(text, maxEntityOccurrences+1) {
		x.add("version", m, "mention", nativeField)
	}
	for _, m := range entityTermPattern.FindAllString(text, maxEntityOccurrences+1) {
		x.add("term", m, "mention", nativeField)
	}
}

func TestTrajectoryEntityTextPrefilterMatchesOriginal(t *testing.T) {
	pieces := []string{"padding", "研究", "\x00", "alpha.go", "file:///repo/a.md", "https://host/a.md", "./skills/alpha/SKILL.md", "../a.ts", "$alpha", "skill:beta", "askill:beta", "tool:read", "Tool:read", "v1.2.3-rc.1", "1.2", ".", "/", "$", "skill", "tool", "é", "<>", " ", "\n", "\t"}
	texts := []string{"", strings.Repeat("padding ", 1024), strings.Repeat("padding ", 2200) + "$last /last.go", strings.Repeat("/repo/a.go $alpha skill:beta tool:read v1.2.3 ", 80)}
	rng := rand.New(rand.NewSource(17))
	for i := 0; i < 250; i++ {
		var body strings.Builder
		for n := rng.Intn(150); n > 0; n-- {
			body.WriteString(pieces[rng.Intn(len(pieces))])
		}
		texts = append(texts, body.String())
	}
	for i, text := range texts {
		for _, cwd := range []string{"", "/repo"} {
			makeExtractor := func() entityExtractor {
				return entityExtractor{event: entityTestEvent("e", "s"), context: EntityContext{Agent: "codex", WorkingDirectory: cwd}, seen: map[string]bool{}}
			}
			want, got := makeExtractor(), makeExtractor()
			originalEntityText(&want, text, "native.content")
			got.text(text, "native.content")
			if !reflect.DeepEqual(got.occurrences, want.occurrences) || !reflect.DeepEqual(got.gaps, want.gaps) {
				t.Fatalf("case %d cwd=%q changed complete entity evidence", i, cwd)
			}
		}
	}
}
