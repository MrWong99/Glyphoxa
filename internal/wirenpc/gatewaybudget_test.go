package wirenpc

import (
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

// budgetSpy captures the classified session-establishment calls.
type budgetSpy struct {
	identifies []string
	resumes    []string
}

func (s *budgetSpy) RecordIdentify(appID string) { s.identifies = append(s.identifies, appID) }
func (s *budgetSpy) RecordResume(appID string)   { s.resumes = append(s.resumes, appID) }

// TestGatewayBudgetListenersClassify proves the disgo Ready dispatch is recorded
// as an IDENTIFY and the Resumed dispatch as a RESUME, each labeled by the
// client's application id (never the token).
func TestGatewayBudgetListenersClassify(t *testing.T) {
	spy := &budgetSpy{}
	listeners := GatewayBudgetListeners(spy)

	appID := snowflake.ID(42)
	client := &bot.Client{ApplicationID: appID}
	gen := events.NewGenericEvent(client, 0, 0)

	ready := &events.Ready{GenericEvent: gen}
	resumed := &events.Resumed{GenericEvent: gen}
	for _, l := range listeners {
		l.OnEvent(ready)
		l.OnEvent(resumed)
	}

	if len(spy.identifies) != 1 || spy.identifies[0] != appID.String() {
		t.Fatalf("identifies = %v, want [%s]", spy.identifies, appID)
	}
	if len(spy.resumes) != 1 || spy.resumes[0] != appID.String() {
		t.Fatalf("resumes = %v, want [%s]", spy.resumes, appID)
	}
}
