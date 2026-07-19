package observe

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// counterValue gathers reg and returns the sample for series `name` at the given
// application_id label, or -1 if that series has no sample yet.
func counterValue(t *testing.T, reg *prometheus.Registry, name, appID string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "application_id" && l.GetValue() == appID {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return -1
}

// TestGatewayBudgetClassifiesIdentifyVsResume proves the two session-establishment
// paths land on SEPARATE series, each labeled by the non-secret application id.
func TestGatewayBudgetClassifiesIdentifyVsResume(t *testing.T) {
	reg := prometheus.NewRegistry()
	b := NewGatewayBudget(reg, 500, slog.New(slog.DiscardHandler))

	b.RecordIdentify("app-1")
	b.RecordIdentify("app-1")
	b.RecordResume("app-1")
	b.RecordIdentify("app-2")

	if got := counterValue(t, reg, "glyphoxa_gateway_identify_total", "app-1"); got != 2 {
		t.Fatalf("identify{app-1} = %v, want 2", got)
	}
	if got := counterValue(t, reg, "glyphoxa_gateway_resume_total", "app-1"); got != 1 {
		t.Fatalf("resume{app-1} = %v, want 1", got)
	}
	if got := counterValue(t, reg, "glyphoxa_gateway_identify_total", "app-2"); got != 1 {
		t.Fatalf("identify{app-2} = %v, want 1", got)
	}
}

// TestGatewayBudgetWarnsWhenIdentifiesExceedThreshold proves a per-application
// rolling window warns once when identifies cross the configured threshold, and
// stays quiet below it.
func TestGatewayBudgetWarnsWhenIdentifiesExceedThreshold(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	b := NewGatewayBudget(prometheus.NewRegistry(), 3, log)

	// Three identifies is AT the threshold — no warning yet.
	for range 3 {
		b.RecordIdentify("app-1")
	}
	if strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("warned at threshold, want quiet:\n%s", buf.String())
	}

	// The fourth crosses it — exactly one warning naming the application id.
	b.RecordIdentify("app-1")
	out := buf.String()
	if n := strings.Count(out, "level=WARN"); n != 1 {
		t.Fatalf("warnings = %d, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "application_id=app-1") {
		t.Fatalf("warning missing application_id:\n%s", out)
	}

	// A fifth while still over threshold must not spam a second warning.
	b.RecordIdentify("app-1")
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 {
		t.Fatalf("warnings after fifth = %d, want 1 (de-duped)", n)
	}
}

// TestGatewayBudgetWindowRollsOff proves identifies older than 24h leave the
// window, so a slow trickle never trips the alarm.
func TestGatewayBudgetWindowRollsOff(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	b := NewGatewayBudget(prometheus.NewRegistry(), 2, log)

	now := time.Now()
	b.now = func() time.Time { return now }

	b.RecordIdentify("app-1")
	b.RecordIdentify("app-1")
	// Advance past the 24h window: the earlier two roll off.
	now = now.Add(25 * time.Hour)
	b.RecordIdentify("app-1")
	b.RecordIdentify("app-1")
	if strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("warned though old identifies rolled off:\n%s", buf.String())
	}
}
