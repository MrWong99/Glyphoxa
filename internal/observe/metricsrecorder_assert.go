package observe

import voice "github.com/MrWong99/Glyphoxa/pkg/voice"

// PrometheusRecorder is the single adapter for BOTH metric contracts. This
// assertion pins that it satisfies pkg/voice's plumbing interface (the
// StageRecorder assertion lives next to the type in prometheus.go). pkg/voice
// does not import internal/observe, so this dependency direction is acyclic.
var _ voice.MetricsRecorder = (*PrometheusRecorder)(nil)
