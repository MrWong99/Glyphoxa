package observe

import (
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

// fakeEnv builds a getenv func over a map, mirroring how the boot helpers'
// tests fake the environment.
func fakeEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// restoreMutexFraction snapshots the process-global mutex sampling fraction
// and restores it when the test ends — SetMutexProfileFraction(-1) reads
// without writing.
func restoreMutexFraction(t *testing.T) {
	t.Helper()
	prev := runtime.SetMutexProfileFraction(-1)
	t.Cleanup(func() { runtime.SetMutexProfileFraction(prev) })
}

// TestSetProfileRatesAppliesMutexFraction pins the knob's happy path: a
// positive integer in GLYPHOXA_PPROF_MUTEX_FRACTION becomes the runtime's
// mutex sampling fraction.
func TestSetProfileRatesAppliesMutexFraction(t *testing.T) {
	restoreMutexFraction(t)

	setProfileRates(fakeEnv(map[string]string{"GLYPHOXA_PPROF_MUTEX_FRACTION": "5"}))
	if got := runtime.SetMutexProfileFraction(-1); got != 5 {
		t.Fatalf("mutex profile fraction = %d, want 5", got)
	}
}

// TestSetProfileRatesIgnoresGarbage pins the failure mode the comment
// promises: unparsable or non-positive values change nothing, so a typo in a
// pod spec degrades to "profile stays empty", never to a crash or an
// accidental every-event sampling rate.
func TestSetProfileRatesIgnoresGarbage(t *testing.T) {
	restoreMutexFraction(t)
	before := runtime.SetMutexProfileFraction(-1)

	for _, v := range []string{"banana", "-3", "0", ""} {
		setProfileRates(fakeEnv(map[string]string{
			"GLYPHOXA_PPROF_BLOCK_RATE":     v,
			"GLYPHOXA_PPROF_MUTEX_FRACTION": v,
		}))
		if got := runtime.SetMutexProfileFraction(-1); got != before {
			t.Fatalf("mutex fraction changed to %d on input %q, want unchanged %d", got, v, before)
		}
	}
}

// blockEvents sums the sampled blocking events across every record of the
// block profile. The profile's Count() is no substitute: it counts distinct
// stacks, so a repeat block at an already-recorded stack leaves it unchanged.
func blockEvents() int64 {
	var recs []runtime.BlockProfileRecord
	n, ok := runtime.BlockProfile(nil)
	for !ok {
		recs = make([]runtime.BlockProfileRecord, n+8)
		n, ok = runtime.BlockProfile(recs)
	}
	var events int64
	for _, r := range recs[:n] {
		events += r.Count
	}
	return events
}

// TestSetProfileRatesArmsBlockProfile proves the block knob end to end: with
// the rate armed, a real blocked channel receive shows up in the block
// profile. There is no read-back API for the block rate, so the profile
// gaining an event is the only honest assertion.
func TestSetProfileRatesArmsBlockProfile(t *testing.T) {
	before := blockEvents()

	setProfileRates(fakeEnv(map[string]string{"GLYPHOXA_PPROF_BLOCK_RATE": "1"}))
	t.Cleanup(func() { runtime.SetBlockProfileRate(0) })

	// Rate 1ns samples every blocking event, but one attempt is a race: if
	// this goroutine loses the scheduler for longer than the helper's sleep
	// (a GC pause, cgroup throttling on a shared runner), close lands first
	// and the receive never blocks at all. So keep generating candidate
	// blocks, with a growing sleep, until one of them registers.
	deadline := time.Now().Add(10 * time.Second)
	for wait := time.Millisecond; blockEvents() <= before; wait *= 2 {
		if time.Now().After(deadline) {
			t.Fatalf("block profile never gained an event (still %d)", blockEvents())
		}
		ch := make(chan struct{})
		go func(d time.Duration) {
			time.Sleep(d)
			close(ch)
		}(wait)
		<-ch
	}
}

// TestPprofGateArmsProfileRates pins the wiring: MountObservability applies
// the rate knobs only inside the GLYPHOXA_PPROF gate, from the real
// environment.
func TestPprofGateArmsProfileRates(t *testing.T) {
	restoreMutexFraction(t)

	t.Setenv("GLYPHOXA_PPROF", "1")
	t.Setenv("GLYPHOXA_PPROF_MUTEX_FRACTION", "7")
	srv := httptest.NewServer(newMux(NewPrometheusRecorder(), nil))
	defer srv.Close()

	if got := runtime.SetMutexProfileFraction(-1); got != 7 {
		t.Fatalf("mutex profile fraction after mount = %d, want 7", got)
	}
}
