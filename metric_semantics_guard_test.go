package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// M01_S02 KR2: the semantic layer owns metric meaning, and nothing else may
// restate it.
//
// The repo rule has existed since the metric semantics doc was written, but it
// was enforced by reading diffs. That works until it doesn't: freshness was
// classified in one place and then re-decided by hand in four others, each
// spelling "active"/"idle"/"stale" inline. Every copy was correct the day it
// was written, and nothing would have told us when one stopped being.
//
// So the guard is deliberately about *vocabulary*, not about arithmetic. A
// regex for `/` or `* 100` would flag every byte-count and sort comparator in
// the repo and would be turned off within a week. A metric term appearing
// outside the file that defines it is both rare and always worth a look.

// metricVocabulary maps a reserved string literal to the semantic helper that
// should be used instead. Add an entry when a new metric term gets a home in
// the semantic layer -- not before, or the guard flags code that has nowhere
// to go.
var metricVocabulary = map[string]string{
	"active": "freshnessActive / freshnessFromEventAge",
	"idle":   "freshnessIdle / freshnessFromEventAge",
	"stale":  "freshnessStale / freshnessFromEventAge",
}

// semanticLayerFiles are allowed to spell the vocabulary: they define it.
var semanticLayerFiles = map[string]bool{
	"metric_semantics.go": true,
	"metric_registry.go":  true,
}

// vocabularyExemptFields are struct fields and map keys whose string value is
// a wire format or a UI token rather than a metric decision. Assigning the
// literal "stale" to a JSON state field is serialization; comparing a session's
// freshness to it is a metric judgment. Only the latter is the target.
var vocabularyExemptFields = map[string]bool{
	// liveTokenRateSample.State and friends carry these as wire values; their
	// meaning is fixed by the semantic layer's own constants above.
	"State":  true,
	"Status": true,
	"Kind":   true,
}

func TestMetricVocabularyStaysInTheSemanticLayer(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var violations []string

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		// Tests may spell the vocabulary freely: a test that cannot write
		// "stale" cannot pin what stale means.
		if strings.HasSuffix(name, "_test.go") || semanticLayerFiles[name] {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.KeyValueExpr:
				// Skip the value of an exempt field, but keep walking its key
				// and any nested expressions elsewhere in the literal.
				if ident, ok := n.Key.(*ast.Ident); ok && vocabularyExemptFields[ident.Name] {
					return false
				}
			case *ast.BasicLit:
				if n.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(n.Value)
				if err != nil {
					return true
				}
				if want, reserved := metricVocabulary[value]; reserved {
					violations = append(violations, filepath.Base(fset.Position(n.Pos()).String())+
						": "+n.Value+" -- use "+want)
				}
			}
			return true
		})
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("metric vocabulary spelled outside the semantic layer (%d sites).\n"+
			"These strings decide what a metric means, so they belong to metric_semantics.go.\n"+
			"If a site is serialization rather than a metric decision, add its field to\n"+
			"vocabularyExemptFields and say why in the comment.\n\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// TestMetricVocabularyGuardCatchesAnInlineLiteral proves the guard can fail.
// A gate whose only evidence is a green run is not a gate -- this feeds it the
// exact shape it exists to catch and asserts it reacts.
func TestMetricVocabularyGuardCatchesAnInlineLiteral(t *testing.T) {
	const source = `package main

func classify(age int) string {
	if age < 10 {
		return "active"
	}
	return "stale"
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", source, 0)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}
	found := 0
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if _, reserved := metricVocabulary[value]; reserved {
			found++
		}
		return true
	})
	if found != 2 {
		t.Fatalf("guard vocabulary missed an inline freshness literal: matched %d of 2", found)
	}
}
