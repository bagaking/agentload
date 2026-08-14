package main

import (
	"fmt"
	"log"
	"runtime/debug"
	"strings"
)

func recoverBackgroundPanic(scope string) {
	if recovered := recover(); recovered != nil {
		log.Printf("%s panic recovered: %s\n%s", strings.TrimSpace(scope),
			sanitizeTextForClient(fmt.Sprint(recovered)),
			sanitizeTextForClient(string(debug.Stack())))
	}
}

// runBackgroundStep contains a panic to a single step of a background loop.
// Deferring recoverBackgroundPanic at the top of a goroutine only stops the
// process from dying: the loop itself unwinds and the service stays dead. Owners
// wrap one iteration in this instead so a bad step is dropped and the next
// iteration still runs.
func runBackgroundStep(scope string, step func()) {
	defer recoverBackgroundPanic(scope)
	step()
}
