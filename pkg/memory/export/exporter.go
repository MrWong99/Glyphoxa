package export

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	"gopkg.in/yaml.v3"
)

const archiveVersion = 1

// ExportData holds all the data needed to write a campaign archive.
// Callers are responsible for gathering this data (typically from the
// database in a REPEATABLE READ transaction).
type ExportData struct {
	CampaignID  string
	TenantID    string
	LicenseTier string

	NPCs          []npcstore.NPCDefinition
	Entities      []memory.Entity
	Relationships []memory.Relationship
	Sessions      map[string][]memory.TranscriptEntry // session_id -> entries
}

// WriteTarGz writes a campaign archive to w. The archive follows the
// directory layout described in the package documentation.
func WriteTarGz(w io.Writer, data ExportData) error {
	gw := gzip.NewWriter(w)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	prefix := fmt.Sprintf("campaign-export-%s", data.CampaignID)

	// Write metadata.json
	meta := Metadata{
		CampaignID:  data.CampaignID,
		TenantID:    data.TenantID,
		LicenseTier: data.LicenseTier,
		ExportedAt:  time.Now().UTC(),
		Version:     archiveVersion,
	}
	if err := writeJSON(tw, prefix+"/metadata.json", meta); err != nil {
		return fmt.Errorf("export: write metadata: %w", err)
	}

	// Write npcs/*.yaml
	for _, npc := range data.NPCs {
		name := sanitizeFilename(npc.Name)
		if name == "" {
			name = npc.ID
		}
		path := fmt.Sprintf("%s/npcs/%s.yaml", prefix, name)
		yamlData, err := yaml.Marshal(npc)
		if err != nil {
			return fmt.Errorf("export: marshal NPC %q: %w", npc.ID, err)
		}
		if err := writeBytes(tw, path, yamlData); err != nil {
			return fmt.Errorf("export: write NPC %q: %w", npc.ID, err)
		}
	}

	// Write knowledge-graph.json
	kgExport := KnowledgeGraphExport{
		Entities:      make([]EntityExport, 0, len(data.Entities)),
		Relationships: make([]RelationshipExport, 0, len(data.Relationships)),
	}
	for _, e := range data.Entities {
		kgExport.Entities = append(kgExport.Entities, EntityExport{
			ID:         e.ID,
			Type:       e.Type,
			Name:       e.Name,
			Attributes: e.Attributes,
			CreatedAt:  e.CreatedAt,
			UpdatedAt:  e.UpdatedAt,
		})
	}
	for _, r := range data.Relationships {
		kgExport.Relationships = append(kgExport.Relationships, RelationshipExport{
			SourceID:   r.SourceID,
			TargetID:   r.TargetID,
			RelType:    r.RelType,
			Attributes: r.Attributes,
			Provenance: ProvenanceExport{
				SessionID:   r.Provenance.SessionID,
				Timestamp:   r.Provenance.Timestamp,
				Confidence:  r.Provenance.Confidence,
				Source:      r.Provenance.Source,
				DMConfirmed: r.Provenance.DMConfirmed,
			},
			CreatedAt: r.CreatedAt,
		})
	}
	if err := writeJSON(tw, prefix+"/knowledge-graph.json", kgExport); err != nil {
		return fmt.Errorf("export: write knowledge graph: %w", err)
	}

	// Write sessions/*.txt
	// Sort session IDs for deterministic output.
	sessionIDs := make([]string, 0, len(data.Sessions))
	for sid := range data.Sessions {
		sessionIDs = append(sessionIDs, sid)
	}
	sort.Strings(sessionIDs)

	for i, sid := range sessionIDs {
		entries := data.Sessions[sid]
		var sb strings.Builder
		for _, e := range entries {
			ts := e.Timestamp.Format(time.RFC3339)
			fmt.Fprintf(&sb, "%s %s: %s\n", ts, e.SpeakerName, e.Text)
		}

		path := fmt.Sprintf("%s/sessions/session-%03d.txt", prefix, i+1)
		if err := writeBytes(tw, path, []byte(sb.String())); err != nil {
			return fmt.Errorf("export: write session %q: %w", sid, err)
		}
	}

	return nil
}

func writeJSON(tw *tar.Writer, path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeBytes(tw, path, data)
}

func writeBytes(tw *tar.Writer, path string, data []byte) error {
	hdr := &tar.Header{
		Name:    path,
		Size:    int64(len(data)),
		Mode:    0644,
		ModTime: time.Now().UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// sanitizeFilename replaces characters unsafe for filenames.
func sanitizeFilename(name string) string {
	r := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		" ", "_",
		".", "_",
		":", "_",
	)
	return strings.ToLower(r.Replace(name))
}
