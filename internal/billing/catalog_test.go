package billing

import (
	"strings"
	"testing"
)

func TestParseCatalogValid(t *testing.T) {
	data := []byte(`{
	  "plans": [
	    {"slug": "free", "display_name": "Free", "monthly_price_usd": 0},
	    {"slug": "byok-pro", "display_name": "BYOK Pro", "monthly_price_usd": 5,
	     "key_source": "byok", "limits": {"max_campaigns": 10}},
	    {"slug": "all-inclusive", "display_name": "All Inclusive",
	     "description": "Groq + ElevenLabs usage included",
	     "monthly_price_usd": 20, "key_source": "platform", "included_usage_usd": 15}
	  ]
	}`)
	c, err := ParseCatalog(data)
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if len(c.Plans) != 3 {
		t.Fatalf("plans = %d, want 3", len(c.Plans))
	}
	// key_source defaults to byok when omitted.
	if c.Plans[0].KeySource != KeySourceBYOK {
		t.Errorf("free key_source = %q, want %q", c.Plans[0].KeySource, KeySourceBYOK)
	}
	if c.Plans[2].KeySource != KeySourcePlatform {
		t.Errorf("all-inclusive key_source = %q, want %q", c.Plans[2].KeySource, KeySourcePlatform)
	}
	if c.Plans[2].IncludedUsageUSD == nil || *c.Plans[2].IncludedUsageUSD != 15 {
		t.Errorf("all-inclusive included_usage_usd = %v, want 15", c.Plans[2].IncludedUsageUSD)
	}
	limits, err := c.Plans[1].LimitsJSON()
	if err != nil {
		t.Fatalf("LimitsJSON: %v", err)
	}
	if string(limits) != `{"max_campaigns":10}` {
		t.Errorf("limits JSON = %s", limits)
	}
	empty, _ := c.Plans[0].LimitsJSON()
	if string(empty) != `{}` {
		t.Errorf("empty limits JSON = %s, want {}", empty)
	}
}

func TestParseCatalogRejects(t *testing.T) {
	cases := []struct {
		name, data, wantErr string
	}{
		{"unknown field", `{"plans":[{"slug":"a","display_name":"A","monthly_price":1}]}`, "unknown field"},
		{"duplicate slug", `{"plans":[{"slug":"a","display_name":"A"},{"slug":"a","display_name":"B"}]}`, "duplicate plan slug"},
		{"bad slug", `{"plans":[{"slug":"Not A Slug","display_name":"A"}]}`, "slug must match"},
		{"missing display name", `{"plans":[{"slug":"a"}]}`, "display_name is required"},
		{"negative price", `{"plans":[{"slug":"a","display_name":"A","monthly_price_usd":-1}]}`, "monthly_price_usd"},
		{"allowance on byok", `{"plans":[{"slug":"a","display_name":"A","included_usage_usd":5}]}`, "only valid on"},
		{"negative allowance", `{"plans":[{"slug":"a","display_name":"A","key_source":"platform","included_usage_usd":-5}]}`, "included_usage_usd"},
		{"unknown key source", `{"plans":[{"slug":"a","display_name":"A","key_source":"pooled"}]}`, "key_source must be"},
		{"trailing data", `{"plans":[]} {"more": true}`, "trailing data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCatalog([]byte(tc.data))
			if err == nil {
				t.Fatalf("ParseCatalog accepted %s", tc.data)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseCatalogEmptyList(t *testing.T) {
	c, err := ParseCatalog([]byte(`{"plans": []}`))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if len(c.Plans) != 0 {
		t.Fatalf("plans = %d, want 0", len(c.Plans))
	}
}
