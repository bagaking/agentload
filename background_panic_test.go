package main

import "testing"

func TestRecoverBackgroundPanicSwallowsPanic(t *testing.T) {
	func() {
		defer recoverBackgroundPanic("test scope")
		panic("boom")
	}()
}
