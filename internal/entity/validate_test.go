package entity

import (
	"testing"
)

func TestEntityType_IsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		et    EntityType
		valid bool
	}{
		{"npc", EntityNPC, true},
		{"location", EntityLocation, true},
		{"item", EntityItem, true},
		{"faction", EntityFaction, true},
		{"quest", EntityQuest, true},
		{"lore", EntityLore, true},
		{"empty", EntityType(""), false},
		{"unknown", EntityType("monster"), false},
		{"uppercase", EntityType("NPC"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.et.IsValid(); got != tt.valid {
				t.Errorf("EntityType(%q).IsValid() = %v, want %v", tt.et, got, tt.valid)
			}
		})
	}
}

func TestValidate_Valid(t *testing.T) {
	t.Parallel()

	ed := EntityDefinition{
		Name: "Greymantle",
		Type: EntityNPC,
	}
	if err := Validate(ed); err != nil {
		t.Errorf("expected nil error for valid entity, got %v", err)
	}
}

func TestValidate_EmptyName(t *testing.T) {
	t.Parallel()

	ed := EntityDefinition{
		Name: "",
		Type: EntityNPC,
	}
	err := Validate(ed)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestValidate_InvalidType(t *testing.T) {
	t.Parallel()

	ed := EntityDefinition{
		Name: "TestEntity",
		Type: EntityType("monster"),
	}
	err := Validate(ed)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestValidate_EmptyNameAndInvalidType(t *testing.T) {
	t.Parallel()

	ed := EntityDefinition{
		Name: "",
		Type: EntityType(""),
	}
	err := Validate(ed)
	if err == nil {
		t.Fatal("expected error for empty name and invalid type")
	}
}

func TestValidate_RelationshipWithEmptyType(t *testing.T) {
	t.Parallel()

	ed := EntityDefinition{
		Name: "TestEntity",
		Type: EntityNPC,
		Relationships: []RelationshipDef{
			{TargetID: "target-1", Type: ""},
		},
	}
	err := Validate(ed)
	if err == nil {
		t.Fatal("expected error for relationship with empty type")
	}
}

func TestValidate_ValidRelationship(t *testing.T) {
	t.Parallel()

	ed := EntityDefinition{
		Name: "TestEntity",
		Type: EntityNPC,
		Relationships: []RelationshipDef{
			{TargetID: "target-1", Type: "ally"},
		},
	}
	err := Validate(ed)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestValidate_MultipleRelationshipErrors(t *testing.T) {
	t.Parallel()

	ed := EntityDefinition{
		Name: "TestEntity",
		Type: EntityNPC,
		Relationships: []RelationshipDef{
			{TargetID: "t1", Type: ""},
			{TargetID: "t2", Type: "ally"},
			{TargetID: "t3", Type: ""},
		},
	}
	err := Validate(ed)
	if err == nil {
		t.Fatal("expected error for relationships with empty types")
	}
}

func TestValidate_WithTags(t *testing.T) {
	t.Parallel()

	ed := EntityDefinition{
		Name:        "Greymantle",
		Type:        EntityNPC,
		Description: "A wise sage",
		Tags:        []string{"wise", "old", "sage"},
	}
	if err := Validate(ed); err != nil {
		t.Errorf("expected nil error for valid entity with tags, got %v", err)
	}
}
