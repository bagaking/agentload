package main

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

var codexRolloutSessionPattern = regexp.MustCompile(`^rollout-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-(.+)$`)

type builtinTranscriptParser struct {
	parse     transcriptParseFunc
	parseTail transcriptParseFunc
	append    transcriptAppendParseFunc
	canAppend func(TranscriptFile) bool
}

func (p builtinTranscriptParser) Parse(file TranscriptFile) (*SessionTrace, error) {
	if p.parse == nil {
		return nil, fmt.Errorf("transcript parsing is unsupported")
	}
	return p.parse(file)
}

func (p builtinTranscriptParser) ParseTail(file TranscriptFile) (*SessionTrace, error) {
	if p.parseTail == nil {
		return nil, fmt.Errorf("transcript tail parsing is unsupported")
	}
	return p.parseTail(file)
}

func (p builtinTranscriptParser) ParseAppend(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error) {
	if p.append == nil {
		return nil, fmt.Errorf("transcript append parsing is unsupported")
	}
	return p.append(file, base, offset)
}

func (p builtinTranscriptParser) CanAppend(file TranscriptFile) bool {
	return p.canAppend != nil && p.canAppend(file)
}

func newClaudeTranscriptParser() agentTranscriptParser {
	return builtinTranscriptParser{
		parse: func(file TranscriptFile) (*SessionTrace, error) {
			return parseClaudeTrace(file.Path)
		},
		parseTail: parseClaudeTraceTail,
		append:    parseClaudeTraceAppend,
		canAppend: func(TranscriptFile) bool { return true },
	}
}

func newCodexTranscriptParser() agentTranscriptParser {
	return builtinTranscriptParser{
		parse: func(file TranscriptFile) (*SessionTrace, error) {
			if isCodexLaneTranscript(file.Path) {
				return parseCodexLaneTrace(file.Path)
			}
			return parseCodexTrace(file.Path)
		},
		parseTail: func(file TranscriptFile) (*SessionTrace, error) {
			if isCodexLaneTranscript(file.Path) {
				return parseCodexLaneTrace(file.Path)
			}
			return parseCodexTraceTail(file)
		},
		append:    parseCodexTraceAppend,
		canAppend: func(file TranscriptFile) bool { return !isCodexLaneTranscript(file.Path) },
	}
}

func newTraeTranscriptParser() agentTranscriptParser {
	return builtinTranscriptParser{
		parse: func(file TranscriptFile) (*SessionTrace, error) {
			return parseTraeTrace(file.Path)
		},
		parseTail: parseTraeTraceTail,
		append:    parseTraeTraceAppend,
		canAppend: func(TranscriptFile) bool { return true },
	}
}

func newGrokTranscriptParser() agentTranscriptParser {
	return builtinTranscriptParser{
		parse: func(file TranscriptFile) (*SessionTrace, error) {
			return parseGrokTrace(file.Path)
		},
		parseTail: parseGrokTraceTail,
		append:    parseGrokTraceAppend,
		canAppend: func(TranscriptFile) bool { return true },
	}
}

func isCodexLaneTranscript(path string) bool {
	return strings.Contains(filepath.Clean(path), string(filepath.Separator)+".codexl"+string(filepath.Separator))
}

// grokTranscriptFileName is the only grok session file with a timestamp on
// every line; see grokTranscriptDiscovery.
const grokTranscriptFileName = "updates.jsonl"

// grokTranscriptSessionID reads the session from the directory holding
// updates.jsonl. Unlike the other vendors the file stem is a constant, so the
// parent directory is this session's only identity.
func grokTranscriptSessionID(path string) string {
	if filepath.Base(path) != grokTranscriptFileName {
		return genericTranscriptSessionID(path)
	}
	return filepath.Base(filepath.Dir(path))
}

// grokWorkdirFromTranscriptPath recovers the working directory grok encoded
// into the session's grandparent directory name ("%2FUsers%2Ffoo" -> "/Users/foo").
// A name that does not decode is not guessed at — the caller then leaves the
// project unassigned rather than inventing one from a partial path.
func grokWorkdirFromTranscriptPath(path string) string {
	encoded := filepath.Base(filepath.Dir(filepath.Dir(path)))
	decoded, err := url.PathUnescape(encoded)
	if err != nil || !strings.HasPrefix(decoded, "/") {
		return ""
	}
	return filepath.Clean(decoded)
}

func genericTranscriptSessionID(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

func codexTranscriptSessionID(path string) string {
	base := genericTranscriptSessionID(path)
	if base == "events" {
		return filepath.Base(filepath.Dir(path))
	}
	if match := codexRolloutSessionPattern.FindStringSubmatch(base); len(match) == 2 {
		return match[1]
	}
	return base
}
