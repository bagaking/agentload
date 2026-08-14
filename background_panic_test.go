package main

import "testing"

func TestRecoverBackgroundPanicSwallowsPanic(t *testing.T) {
	func() {
		defer recoverBackgroundPanic("test scope")
		panic("boom")
	}()
}

func TestRunBackgroundStepContainsPanicAndKeepsLoopAlive(t *testing.T) {
	steps := 0
	for i := 0; i < 3; i++ {
		runBackgroundStep("test scope", func() {
			steps++
			if steps == 1 {
				panic("boom")
			}
		})
	}
	// The panicking step must not unwind the caller's loop: all three
	// iterations run, which is what distinguishes containing a panic from
	// merely keeping the process alive.
	if steps != 3 {
		t.Fatalf("expected all 3 steps to run after a panic in the first, got %d", steps)
	}
}

func TestRunBackgroundStepRunsDeferredReleasesOfCaller(t *testing.T) {
	released := false
	func() {
		defer func() { released = true }()
		runBackgroundStep("test scope", func() { panic("boom") })
	}()
	if !released {
		t.Fatal("expected the caller's deferred release to run after a contained panic")
	}
}
