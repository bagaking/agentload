package trajectory

import (
	stdjson "encoding/json"
	"reflect"
	"strings"
	"testing"
)

// A source-backed index regenerates facts; changing JSON edge behavior would
// silently change those facts even when its storage format stays the same.
func TestTrajectoryNativeJSONPreservesStandardFieldSemantics(t *testing.T) {
	values := []string{
		`null`, `42`, `true`, `[]`, `{"x":1}`, `"plain"`,
		`"line\nquote\"slash\/tab\t"`, `"\uD83D\uDE0A"`, `"\uD800"`,
		`"\uDC00"`, `"\u2028\u2029<&>"`, `""`,
		"\"" + strings.Repeat("large field ", 20000) + "\"",
		string([]byte{'"', 0xff, '"'}),
	}
	for _, raw := range values {
		var text string
		err := stdjson.Unmarshal([]byte(raw), &text)
		if err != nil {
			text = raw
		}
		if raw == "null" {
			text = ""
		}
		if got := String([]byte(raw)); got != text {
			t.Fatalf("field changed: %q != %q", got, text)
		}
	}
	objects := []string{
		`null`, `[]`, `42`, `{"value":null}`, `{"value":"first","value":"last"}`,
		`{"payload":{"a":1},"payload":{"b":2}}`,
		`{"value":"\uD800"}`, `{"value":"\uD83D\uDE0A"}`,
		`{"value":"good","broken":]`, `{"value":"bad\x41"}`,
	}
	for _, raw := range objects {
		var want map[string]stdjson.RawMessage
		_ = stdjson.Unmarshal([]byte(raw), &want)
		if got := object([]byte(raw)); !reflect.DeepEqual(got, want) {
			t.Fatalf("native object changed for %q: %v != %v", raw, got, want)
		}
	}
}
