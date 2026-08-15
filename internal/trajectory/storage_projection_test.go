package trajectory

import (
	"agentload/internal/snapshot"
	"testing"
)

func TestTrajectoryCompactPostingCandidatesRequireExactLiteralAndGlobEscapes(t *testing.T) {
	s, _ := fixture(t, request("abc something bcd")+request("abcd a*b [literal] a?b 中文词")+request("after\x00abcd"))
	for _, term := range []string{"abcd", "a*b", "[literal]", "a?b", "中文词", "after\x00abcd"} {
		q := snapshot.TrajectorySelector{Collection: "events", Text: term}
		got := searchTestPreparedQuery(t, s, q)
		want := searchTestBaseline(t, s, q)
		if len(got.Events) != len(want) {
			t.Fatal("literal query differs", term, len(got.Events), len(want))
		}
		for i := range want {
			if got.Events[i].ID != want[i] {
				t.Fatal("literal identity differs")
			}
		}
	}
}
