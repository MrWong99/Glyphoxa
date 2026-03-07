package export

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	"gopkg.in/yaml.v3"
)

// ImportData holds the parsed contents of a campaign archive.
// The caller is responsible for persisting the data to the appropriate stores
// and re-indexing embeddings.
type ImportData struct {
	Metadata      Metadata
	NPCs          []npcstore.NPCDefinition
	Entities      []memory.Entity
	Relationships []memory.Relationship
	Sessions      map[string][]memory.TranscriptEntry // filename -> entries
}

// ReadTarGz reads and validates a campaign archive from r.
// Returns the parsed data for the caller to persist.
func ReadTarGz(r io.Reader) (*ImportData, error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("import: open gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	data := &ImportData{
		Sessions: make(map[string][]memory.TranscriptEntry),
	}

	var foundMetadata bool

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("import: read tar: %w", err)
		}

		// Skip directories.
		if hdr.Typeflag == tar.TypeDir {
			continue
		}

		// Strip the top-level directory prefix.
		name := hdr.Name
		parts := strings.SplitN(name, "/", 2)
		if len(parts) < 2 {
			continue
		}
		relPath := parts[1]

		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("import: read %q: %w", name, err)
		}

		switch {
		case relPath == "metadata.json":
			if err := json.Unmarshal(content, &data.Metadata); err != nil {
				return nil, fmt.Errorf("import: parse metadata: %w", err)
			}
			foundMetadata = true

		case strings.HasPrefix(relPath, "npcs/") && strings.HasSuffix(relPath, ".yaml"):
			var npc npcstore.NPCDefinition
			if err := yaml.Unmarshal(content, &npc); err != nil {
				return nil, fmt.Errorf("import: parse NPC %q: %w", relPath, err)
			}
			data.NPCs = append(data.NPCs, npc)

		case relPath == "knowledge-graph.json":
			var kg KnowledgeGraphExport
			if err := json.Unmarshal(content, &kg); err != nil {
				return nil, fmt.Errorf("import: parse knowledge graph: %w", err)
			}
			for _, e := range kg.Entities {
				data.Entities = append(data.Entities, memory.Entity{
					ID:         e.ID,
					Type:       e.Type,
					Name:       e.Name,
					Attributes: e.Attributes,
					CreatedAt:  e.CreatedAt,
					UpdatedAt:  e.UpdatedAt,
				})
			}
			for _, r := range kg.Relationships {
				data.Relationships = append(data.Relationships, memory.Relationship{
					SourceID:   r.SourceID,
					TargetID:   r.TargetID,
					RelType:    r.RelType,
					Attributes: r.Attributes,
					Provenance: memory.Provenance{
						SessionID:   r.Provenance.SessionID,
						Timestamp:   r.Provenance.Timestamp,
						Confidence:  r.Provenance.Confidence,
						Source:      r.Provenance.Source,
						DMConfirmed: r.Provenance.DMConfirmed,
					},
					CreatedAt: r.CreatedAt,
				})
			}

		case strings.HasPrefix(relPath, "sessions/") && strings.HasSuffix(relPath, ".txt"):
			entries, err := parseSessionTxt(content)
			if err != nil {
				return nil, fmt.Errorf("import: parse session %q: %w", relPath, err)
			}
			sessionName := strings.TrimSuffix(path.Base(relPath), ".txt")
			data.Sessions[sessionName] = entries
		}
	}

	if !foundMetadata {
		return nil, fmt.Errorf("import: archive missing metadata.json")
	}

	return data, nil
}

// parseSessionTxt parses session transcript lines in the format:
// <RFC3339 timestamp> <speaker_name>: <text>
func parseSessionTxt(content []byte) ([]memory.TranscriptEntry, error) {
	var entries []memory.TranscriptEntry
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Format: "2026-01-15T20:30:00Z Player Name: some text"
		tsStr, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}

		ts, err := time.Parse(time.RFC3339, tsStr)
		if err != nil {
			continue
		}

		speaker, text, ok := strings.Cut(rest, ": ")
		if !ok {
			continue
		}

		entries = append(entries, memory.TranscriptEntry{
			SpeakerName: speaker,
			Text:        text,
			Timestamp:   ts,
		})
	}
	return entries, nil
}
