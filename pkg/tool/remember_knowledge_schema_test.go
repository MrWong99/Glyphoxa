package tool

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
)

// TestRememberKnowledgeSchema_EnumsEqualVocabulary is the #449 drift pin: the
// JSON schema remember_knowledge DECLARES to the LLM must carry exactly the
// kgvocab vocabularies — kinds, relations, node types — in canonical order. The
// schema is built from the vocabulary at package init, so this asserts the
// derivation stays wired (a hand-edited enum string would fail here).
func TestRememberKnowledgeSchema_EnumsEqualVocabulary(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(NewRememberKnowledge(nil).InputSchema(), &schema); err != nil {
		t.Fatalf("declared input schema is not valid JSON: %v", err)
	}

	want := map[string][]string{
		"kind":      {kgvocab.KindFact, kgvocab.KindEdge, kgvocab.KindNode},
		"relation":  kgvocab.Relations(),
		"node_type": kgvocab.NodeTypes(),
	}
	for field, wantEnum := range want {
		prop, ok := schema.Properties[field]
		if !ok {
			t.Errorf("schema has no %q property", field)
			continue
		}
		if !slices.Equal(prop.Enum, wantEnum) {
			t.Errorf("schema %q enum = %v, want the kgvocab vocabulary %v", field, prop.Enum, wantEnum)
		}
	}
}

// TestRememberKnowledgeSchema_ValidationMatchesVocabulary closes the other half
// of the #449 loop: every value the schema advertises is accepted by the
// handler's validation, so the declared contract and the enforced one are the
// same set (adding a relation/node type in kgvocab flips both at once).
func TestRememberKnowledgeSchema_ValidationMatchesVocabulary(t *testing.T) {
	for _, r := range kgvocab.Relations() {
		if err := validateArgs(rememberArgs{Kind: kgvocab.KindEdge, Subject: "A", Relation: r, Target: "B"}); err != nil {
			t.Errorf("validateArgs rejected advertised relation %q: %v", r, err)
		}
	}
	for _, nt := range kgvocab.NodeTypes() {
		if err := validateArgs(rememberArgs{Kind: kgvocab.KindNode, Name: "X", NodeType: nt}); err != nil {
			t.Errorf("validateArgs rejected advertised node_type %q: %v", nt, err)
		}
	}
}
