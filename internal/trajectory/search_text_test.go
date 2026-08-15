package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"
)

func TestTrajectorySearchStreamingLiteralBoundariesAndCompleteChecksum(t *testing.T) {
	text := strings.Repeat("x", 32765) + "跨界a*b\x00research" + strings.Repeat("z", 40000)
	frame, err := encodeTextStored(text)
	if err != nil {
		t.Fatal(err)
	}
	for _, terms := range [][]string{{"跨界a*b\x00research"}, {"research", "跨界"}, {"不存在"}, {"research", "absent"}} {
		want := true
		needles := [][]byte{}
		for _, term := range terms {
			needles = append(needles, []byte(term))
			want = want && strings.Contains(text, term)
		}
		got, err := storedTextMatches(context.Background(), frame, needles, make([]byte, 32*1024+1024))
		if err != nil || got != want {
			t.Fatal("streamed literal mismatch", terms, got, want, err)
		}
	}
	damaged := bytes.Clone(frame)
	damaged[len(damaged)-1] ^= 1
	if _, err := storedTextMatches(context.Background(), damaged, [][]byte{[]byte("x")}, make([]byte, 32*1024+1024)); err == nil {
		t.Fatal("early hit skipped final checksum")
	}
	short := bytes.Clone(frame)
	binary.BigEndian.PutUint32(short[4:8], 1)
	if _, err := storedTextMatches(context.Background(), short, [][]byte{[]byte("x")}, make([]byte, 32*1024+1024)); err == nil {
		t.Fatal("declared expansion size ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := storedTextMatches(ctx, frame, [][]byte{[]byte("x")}, make([]byte, 32*1024+1024)); err != context.Canceled {
		t.Fatal("stream ignored cancellation", err)
	}
}

func TestTrajectorySearchWorkerFailureDiscardsTemporaryMatches(t *testing.T) {
	s, _ := fixture(t, request(strings.Repeat("research ", 500))+request("research other"))
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "research"})
	var row int64
	var body []byte
	if err := s.search.db.QueryRow("SELECT rowid,body FROM ranges ORDER BY rowid LIMIT 1").Scan(&row, &body); err != nil {
		t.Fatal(err)
	}
	body[len(body)-1] ^= 1
	if _, err := s.search.db.Exec("UPDATE ranges SET body=? WHERE rowid=?", body, row); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Query(context.Background(), snapshot.TrajectorySelector{Text: "research"}); err == nil {
		t.Fatal("damaged search body produced a count")
	}
	var remains bool
	if err := s.search.db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_temp_master WHERE name='trajectory_text_matches')").Scan(&remains); err != nil || remains {
		t.Fatal("failed query retained partial matches", err)
	}
}

func TestTrajectoryPlainLiteralMatchesWholeTextAcrossSegments(t *testing.T) {
	for _, text := range []string{
		"", strings.Repeat("x", 32765) + "跨界KRESEARCH suffix",
		"RESEARCH " + strings.Repeat("正文", 70000), "A\xffRESEARCH",
	} {
		for _, args := range []string{"", `{"skill":"BAGAKIT-RESEARCHER"}`, "A\xfftail"} {
			for _, tool := range []bool{false, true} {
				if !tool && args != "" {
					continue
				}
				raw, err := factContentBytes(text, []byte(args))
				if err != nil {
					t.Fatal(err)
				}
				baseline := factSearchText(text, "RUNNER", []byte(args), tool)
				for _, terms := range [][]string{{}, {""}, {"research"}, {"跨界kresearch"}, {"research", "bagakit-researcher"}, {"suffix runner "}, {"tail"}, {"absent"}} {
					want := true
					needles := make([][]byte, len(terms))
					for i, term := range terms {
						needles[i] = []byte(term)
						want = want && strings.Contains(baseline, term)
					}
					got, err := factPlainTextMatches(context.Background(), raw, "RUNNER", tool, needles)
					if err != nil || got != want {
						t.Fatalf("literal mismatch tool=%t terms=%q got=%t want=%t err=%v", tool, terms, got, want, err)
					}
				}
			}
		}
	}
	raw, _ := factContentBytes("research", nil)
	binary.BigEndian.PutUint32(raw[:4], 1)
	if _, err := factPlainTextMatches(context.Background(), raw, "", false, [][]byte{[]byte("research")}); err == nil {
		t.Fatal("early match accepted invalid content extent")
	}
}

func TestTrajectoryBlockReaderReusePreservesCompleteEvidence(t *testing.T) {
	ctx := context.Background()
	buffer := make([]byte, factBlockBytes)
	for _, text := range []string{"", "plain", strings.Repeat("research 跨界", 4000), strings.Repeat("oversized ", 10000)} {
		frame, err := encodeTextStored(text)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeFactBlockInto(ctx, frame, buffer)
		if err != nil || string(got) != text {
			t.Fatal("reused reader changed evidence", len(text), err)
		}
		if frame[3] == 4 {
			damaged := bytes.Clone(frame)
			damaged[len(damaged)-1] ^= 1
			if _, err := decodeFactBlockInto(ctx, damaged, buffer); err == nil {
				t.Fatal("reused reader accepted damaged checksum")
			}
		}
		for _, size := range []uint32{uint32(len(text) + 1), 0} {
			if int(size) == len(text) {
				continue
			}
			wrong := bytes.Clone(frame)
			binary.BigEndian.PutUint32(wrong[4:8], size)
			if _, err := decodeFactBlockInto(ctx, wrong, buffer); err == nil {
				t.Fatal("reused reader accepted wrong declared extent")
			}
		}
	}
	frame, _ := encodeTextStored(strings.Repeat("research ", 10000))
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := decodeFactBlockInto(ctx, frame, buffer); err != context.Canceled {
		t.Fatal("reused reader ignored cancellation", err)
	}
}
