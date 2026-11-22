package llmbench

import (
	"encoding/json"
	"strings"
	"testing"
)

const diceSchema = `{"type":"object","properties":{"count":{"type":"integer","minimum":1,"maximum":100},"sides":{"type":"integer","minimum":2}},"required":["count","sides"]}`

func TestValidateInput(t *testing.T) {
	closed := `{"type":"object","properties":{"sessions":{"type":"integer","minimum":1,"maximum":3}},"additionalProperties":false}`
	enum := `{"type":"object","properties":{"kind":{"type":"string","enum":["fact","edge"]},"fact":{"type":"string"}},"required":["kind"]}`
	for _, tc := range []struct {
		name, schema, input, wantErr string
	}{
		{"valid", diceSchema, `{"count":2,"sides":6}`, ""},
		{"not json", diceSchema, `{"count":2,`, "not valid JSON"},
		{"not an object", diceSchema, `[2,6]`, "object"},
		{"missing required", diceSchema, `{"count":2}`, `"sides"`},
		{"wrong type", diceSchema, `{"count":"2","sides":6}`, `"count"`},
		{"fraction for integer", diceSchema, `{"count":2.5,"sides":6}`, `"count"`},
		{"below minimum", diceSchema, `{"count":0,"sides":6}`, "minimum"},
		{"above maximum", diceSchema, `{"count":101,"sides":6}`, "maximum"},
		{"empty object, nothing required", closed, `{}`, ""},
		{"unknown property on a closed schema", closed, `{"session":1}`, `"session"`},
		{"unknown property on an open schema", diceSchema, `{"count":1,"sides":6,"x":1}`, ""},
		{"enum hit", enum, `{"kind":"fact","fact":"x"}`, ""},
		{"enum miss", enum, `{"kind":"note"}`, "enum"},
	} {
		err := ValidateInput(json.RawMessage(tc.schema), json.RawMessage(tc.input))
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: err = %v, want it to mention %s", tc.name, err, tc.wantErr)
		}
	}
}
