package memory_test

import (
	"slices"
	"testing"

	"github.com/MrWong99/glyphoxa/pkg/memory"
)

func TestApplyRelQueryOpts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []memory.RelQueryOpt
		want memory.RelQueryParams
	}{
		{
			name: "no options yields zero value",
			opts: nil,
			want: memory.RelQueryParams{},
		},
		{
			name: "WithRelTypes sets rel types",
			opts: []memory.RelQueryOpt{memory.WithRelTypes("knows", "hates")},
			want: memory.RelQueryParams{
				RelTypes: []string{"knows", "hates"},
			},
		},
		{
			name: "WithIncoming sets direction in",
			opts: []memory.RelQueryOpt{memory.WithIncoming()},
			want: memory.RelQueryParams{
				DirectionIn: true,
			},
		},
		{
			name: "WithOutgoing sets direction out",
			opts: []memory.RelQueryOpt{memory.WithOutgoing()},
			want: memory.RelQueryParams{
				DirectionOut: true,
			},
		},
		{
			name: "WithRelLimit sets limit",
			opts: []memory.RelQueryOpt{memory.WithRelLimit(42)},
			want: memory.RelQueryParams{
				Limit: 42,
			},
		},
		{
			name: "multiple options combined",
			opts: []memory.RelQueryOpt{
				memory.WithRelTypes("owns"),
				memory.WithRelTypes("member_of"),
				memory.WithIncoming(),
				memory.WithOutgoing(),
				memory.WithRelLimit(10),
			},
			want: memory.RelQueryParams{
				RelTypes:     []string{"owns", "member_of"},
				DirectionIn:  true,
				DirectionOut: true,
				Limit:        10,
			},
		},
		{
			name: "WithRelTypes called multiple times appends",
			opts: []memory.RelQueryOpt{
				memory.WithRelTypes("a", "b"),
				memory.WithRelTypes("c"),
			},
			want: memory.RelQueryParams{
				RelTypes: []string{"a", "b", "c"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := memory.ApplyRelQueryOpts(tt.opts)

			if !slices.Equal(got.RelTypes, tt.want.RelTypes) {
				t.Errorf("RelTypes = %v, want %v", got.RelTypes, tt.want.RelTypes)
			}
			if got.DirectionIn != tt.want.DirectionIn {
				t.Errorf("DirectionIn = %v, want %v", got.DirectionIn, tt.want.DirectionIn)
			}
			if got.DirectionOut != tt.want.DirectionOut {
				t.Errorf("DirectionOut = %v, want %v", got.DirectionOut, tt.want.DirectionOut)
			}
			if got.Limit != tt.want.Limit {
				t.Errorf("Limit = %d, want %d", got.Limit, tt.want.Limit)
			}
		})
	}
}

func TestApplyTraversalOpts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []memory.TraversalOpt
		want memory.TraversalParams
	}{
		{
			name: "no options yields zero value",
			opts: nil,
			want: memory.TraversalParams{},
		},
		{
			name: "TraverseRelTypes sets rel types",
			opts: []memory.TraversalOpt{memory.TraverseRelTypes("knows", "hates")},
			want: memory.TraversalParams{
				RelTypes: []string{"knows", "hates"},
			},
		},
		{
			name: "TraverseNodeTypes sets node types",
			opts: []memory.TraversalOpt{memory.TraverseNodeTypes("npc", "location")},
			want: memory.TraversalParams{
				NodeTypes: []string{"npc", "location"},
			},
		},
		{
			name: "TraverseMaxNodes sets max nodes",
			opts: []memory.TraversalOpt{memory.TraverseMaxNodes(100)},
			want: memory.TraversalParams{
				MaxNodes: 100,
			},
		},
		{
			name: "multiple options combined",
			opts: []memory.TraversalOpt{
				memory.TraverseRelTypes("owns"),
				memory.TraverseNodeTypes("item"),
				memory.TraverseMaxNodes(50),
			},
			want: memory.TraversalParams{
				RelTypes:  []string{"owns"},
				NodeTypes: []string{"item"},
				MaxNodes:  50,
			},
		},
		{
			name: "TraverseRelTypes called multiple times appends",
			opts: []memory.TraversalOpt{
				memory.TraverseRelTypes("a", "b"),
				memory.TraverseRelTypes("c"),
			},
			want: memory.TraversalParams{
				RelTypes: []string{"a", "b", "c"},
			},
		},
		{
			name: "TraverseNodeTypes called multiple times appends",
			opts: []memory.TraversalOpt{
				memory.TraverseNodeTypes("npc"),
				memory.TraverseNodeTypes("location", "item"),
			},
			want: memory.TraversalParams{
				NodeTypes: []string{"npc", "location", "item"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := memory.ApplyTraversalOpts(tt.opts)

			if !slices.Equal(got.RelTypes, tt.want.RelTypes) {
				t.Errorf("RelTypes = %v, want %v", got.RelTypes, tt.want.RelTypes)
			}
			if !slices.Equal(got.NodeTypes, tt.want.NodeTypes) {
				t.Errorf("NodeTypes = %v, want %v", got.NodeTypes, tt.want.NodeTypes)
			}
			if got.MaxNodes != tt.want.MaxNodes {
				t.Errorf("MaxNodes = %d, want %d", got.MaxNodes, tt.want.MaxNodes)
			}
		})
	}
}
