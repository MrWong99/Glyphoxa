//go:build race

package silero_test

// raceEnabled reports that this test binary was built with the race detector,
// whose instrumentation slows the pure-Go forward pass by roughly an order of
// magnitude — meaningless for the wall-clock latency budget.
const raceEnabled = true
