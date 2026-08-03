package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeSpatial records what the Tools asked for and returns canned places.
type fakeSpatial struct {
	places     []Place
	err        error
	gotAgentID string
	gotName    string
	gotRadius  float64
	gotLimit   int
	calls      int
}

func (f *fakeSpatial) Locate(_ context.Context, agentID, name string) ([]Place, error) {
	f.calls++
	f.gotAgentID, f.gotName = agentID, name
	return f.places, f.err
}

func (f *fakeSpatial) Nearby(_ context.Context, agentID string, radius float64, limit int) ([]Place, error) {
	f.calls++
	f.gotAgentID, f.gotRadius, f.gotLimit = agentID, radius, limit
	return f.places, f.err
}

// TestSpatialToolsAreReadOnly pins the ADR-0030 shape: these Tools only read, so
// they run inline within the turn rather than deferring to turn commit. They are
// also scope-supporting (ADR-0029) — an innkeeper knowing the town's layout is
// correct; the same innkeeper knowing the enemy capital's is not.
func TestSpatialToolsAreReadOnly(t *testing.T) {
	for _, tl := range []Tool{NewLocateEntity(nil), NewWhatsNearby(nil)} {
		if !tl.ReadOnly() {
			t.Errorf("%s must be read-only", tl.Name())
		}
		if scoped, ok := tl.(interface{ SupportsScope() bool }); !ok || !scoped.SupportsScope() {
			t.Errorf("%s must support grant scoping", tl.Name())
		}
		// A proposal-mediated write would be a contradiction here.
		if _, ok := tl.(ProposalMediated); ok {
			t.Errorf("%s must not be proposal-mediated; it writes nothing", tl.Name())
		}
	}
}

// TestLocateEntity_ScopesToTheCaller pins that the Agent whose grant is being
// exercised comes from the TURN CONTEXT, never from the arguments — a crafted
// name cannot make one NPC read as another.
func TestLocateEntity_ScopesToTheCaller(t *testing.T) {
	src := &fakeSpatial{places: []Place{{Name: "Rusty Anchor", Kind: "Location", MapName: "Saltmarsh"}}}
	tl := NewLocateEntity(src)
	ctx := WithCaller(context.Background(), "agent-9")

	out, err := tl.Execute(ctx, json.RawMessage(`{"name":"Rusty Anchor"}`), cfgOwnNode)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if src.gotAgentID != "agent-9" {
		t.Errorf("read scoped to %q, want the turn's caller", src.gotAgentID)
	}
	if src.gotName != "Rusty Anchor" {
		t.Errorf("name = %q", src.gotName)
	}
	if !strings.Contains(out, "Saltmarsh") {
		t.Errorf("result = %q, want the map named", out)
	}
}

// TestLocateEntity_UnknownIsAnAnswer pins that not knowing is reported as not
// knowing — better than an invented location, which is what an LLM would supply
// if the Tool errored.
func TestLocateEntity_UnknownIsAnAnswer(t *testing.T) {
	tl := NewLocateEntity(&fakeSpatial{})
	out, err := tl.Execute(WithCaller(context.Background(), "a1"),
		json.RawMessage(`{"name":"Atlantis"}`), cfgOwnNode)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "do not know") {
		t.Errorf("result = %q, want an explicit not-known answer", out)
	}
}

func TestLocateEntity_Validation(t *testing.T) {
	t.Run("empty name is refused", func(t *testing.T) {
		src := &fakeSpatial{}
		_, err := NewLocateEntity(src).Execute(WithCaller(context.Background(), "a1"),
			json.RawMessage(`{"name":"  "}`), cfgOwnNode)
		if err == nil {
			t.Error("expected an error for an empty name")
		}
		if src.calls != 0 {
			t.Error("the read fired for an empty name")
		}
	})

	t.Run("nil source reports unavailable", func(t *testing.T) {
		_, err := NewLocateEntity(nil).Execute(context.Background(),
			json.RawMessage(`{"name":"x"}`), cfgOwnNode)
		if err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Errorf("err = %v, want an unavailable report", err)
		}
	})

	t.Run("a read failure surfaces", func(t *testing.T) {
		src := &fakeSpatial{err: errors.New("db down")}
		_, err := NewLocateEntity(src).Execute(WithCaller(context.Background(), "a1"),
			json.RawMessage(`{"name":"x"}`), cfgOwnNode)
		if err == nil {
			t.Error("expected the read failure to surface")
		}
	})
}

// TestWhatsNearby_DefaultsAndCaps pins that a missing or absurd radius falls back
// to the default rather than failing — "look around" is a question with an obvious
// default — and that the result count is capped so an NPC can actually say it.
func TestWhatsNearby_DefaultsAndCaps(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"no arguments", `{}`},
		{"zero radius", `{"radius":0}`},
		{"absurd radius", `{"radius":42}`},
		{"garbage", `not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSpatial{places: []Place{{Name: "The docks", Kind: "Location"}}}
			if _, err := NewWhatsNearby(src).Execute(WithCaller(context.Background(), "a1"),
				json.RawMessage(tc.args), cfgOwnNode); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if src.gotRadius != DefaultNearbyRadius {
				t.Errorf("radius = %v, want the default %v", src.gotRadius, DefaultNearbyRadius)
			}
			if src.gotLimit != MaxNearbyResults {
				t.Errorf("limit = %d, want the cap %d", src.gotLimit, MaxNearbyResults)
			}
		})
	}
}

func TestWhatsNearby_HonoursAGivenRadius(t *testing.T) {
	src := &fakeSpatial{}
	if _, err := NewWhatsNearby(src).Execute(WithCaller(context.Background(), "a1"),
		json.RawMessage(`{"radius":0.05}`), cfgOwnNode); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if src.gotRadius != 0.05 {
		t.Errorf("radius = %v, want the requested 0.05", src.gotRadius)
	}
}

func TestWhatsNearby_EmptyIsAnAnswer(t *testing.T) {
	out, err := NewWhatsNearby(&fakeSpatial{}).Execute(WithCaller(context.Background(), "a1"),
		json.RawMessage(`{}`), cfgOwnNode)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "nothing else nearby") {
		t.Errorf("result = %q, want an explicit nothing-nearby answer", out)
	}
}

// TestSpatialToolsRejectMisconfiguredGrant pins that a broken grant config fails
// LOUDLY rather than silently widening what an Agent can see.
func TestSpatialToolsRejectMisconfiguredGrant(t *testing.T) {
	src := &fakeSpatial{}
	_, err := NewWhatsNearby(src).Execute(WithCaller(context.Background(), "a1"),
		json.RawMessage(`{}`), "not-a-config")
	if err == nil {
		t.Error("a misconfigured grant must fail loudly")
	}
	if src.calls != 0 {
		t.Error("the read fired despite a misconfigured grant")
	}
}
