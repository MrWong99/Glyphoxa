package main

import (
	"reflect"
	"testing"
)

// TestDrainVoiceWorkerOrder covers sequence (6): on SIGTERM the -mode voice worker
// tears down in exactly one order — stop claiming and serving, then Finish the live
// intent rows (Manager Shutdown), then release the presence-owner claim, then close
// the Discord clients. The owner claim releasing AFTER the Manager finishes is the
// load-bearing property (#492, ADR-0057 (c)): a survivor must not start dispatching
// this instance's interactions until its sessions are wound down.
func TestDrainVoiceWorkerOrder(t *testing.T) {
	var order []string
	drainVoiceWorker(
		func() { order = append(order, "run") },
		func() { order = append(order, "finish") },
		func() { order = append(order, "release-owner") },
		func() { order = append(order, "close-clients") },
	)

	want := []string{"run", "finish", "release-owner", "close-clients"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("drain order = %v, want %v (owner released only after the Manager finishes its rows)", order, want)
	}
}
