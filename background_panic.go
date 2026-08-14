package main

import (
	"log"
	"runtime/debug"
	"strings"
)

func recoverBackgroundPanic(scope string) {
	if recovered := recover(); recovered != nil {
		log.Printf("%s panic recovered: %v\n%s", strings.TrimSpace(scope), recovered, debug.Stack())
	}
}
