package main

import (
	"fmt"
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

func isCodexLaneTranscript(path string) bool {
	return strings.Contains(filepath.Clean(path), string(filepath.Separator)+".codexl"+string(filepath.Separator))
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
