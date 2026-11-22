package llmbench

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// world is the fake campaign behind the knowledge Tools: a handful of German
// facts, transcript lines and a recap, enough for every lookup in the scripts
// to have a real answer. It implements every [tool.Deps] seam the granted Tools
// need, so they are callable without a database.
type world struct {
	mu        sync.Mutex
	proposals []tool.ProposedWrite
}

// agentID is the bench NPC's Agent id; nodeID its linked Knowledge Graph node.
const (
	agentID = "agent-flo"
	nodeID  = "node-flo"
	npcName = "Flo"
)

var worldFacts = []tool.KGFact{
	{Name: "Flo", Type: "Character", Body: "Wirtin der Taverne Zur Goldenen Gans in Brückenau. Kennt jeden im Dorf. Ihr Eintopf ist weit über das Tal hinaus bekannt."},
	{Name: "Gellan Primwasser", Type: "Character", Body: "Fahrender Händler aus Tiefwasser. War letzte Woche in der Goldenen Gans und suchte einen Führer zum alten Stollen. Zahlt immer mit fremden Münzen."},
	{Name: "Der alte Stollen", Type: "Location", Body: "Verlassene Silbermine nördlich von Brückenau, seit dem Einsturz vor zehn Jahren gesperrt. Man erzählt sich, dass man dort nachts Hämmern hört."},
	{Name: "Hilda Eisenfaust", Type: "Character", Body: "Schmiedin von Brückenau. Führt die Schmiede am Marktplatz, seit ihr Vater gestorben ist. Wortkarg, aber ehrlich."},
	{Name: "Der Fremde im roten Mantel", Type: "Character", Body: "Tauchte vor drei Tagen in Brückenau auf. Fragt überall nach alten Karten der Mine und schläft lieber im Stall als in einem Zimmer."},
}

var worldLines = []tool.TranscriptHit{
	{Who: "Gellan Primwasser", Kind: "utterance", Text: "Ich suche jemanden, der mich zum alten Stollen führt. Ich zahle gut, aber nur in Münzen aus Tiefwasser."},
	{Who: "Brak", Kind: "utterance", Text: "Flo, beim nächsten Mal bringe ich dir ein Fass Zwergenbräu mit, versprochen."},
	{Who: "Mira", Kind: "utterance", Text: "Am Markttag würde ich gern bei dir spielen, wenn du mich lässt."},
}

const worldRecap = "In der letzten Sitzung sind Mira und Brak dem Händler Gellan Primwasser zum alten Stollen gefolgt. " +
	"Am Eingang fanden sie frische Spuren und eine zerbrochene Laterne, und aus der Tiefe war Hämmern zu hören. " +
	"Sie sind umgekehrt, bevor es dunkel wurde, und haben beschlossen, mit besserer Ausrüstung zurückzukommen."

func (w *world) deps() tool.Deps {
	return tool.Deps{Transcripts: w, KG: w, KGW: w, Recap: w}
}

// RecapLastSessions implements [tool.Recapper].
func (w *world) RecapLastSessions(context.Context, int) (string, error) { return worldRecap, nil }

// OwnNodeFacts implements [tool.KGReader]: Flo's node and its neighbourhood.
func (w *world) OwnNodeFacts(context.Context, string) ([]tool.KGFact, error) {
	return worldFacts, nil
}

// SearchFacts implements [tool.KGReader].
func (w *world) SearchFacts(_ context.Context, query string, limit int) ([]tool.KGFact, error) {
	var out []tool.KGFact
	for _, f := range worldFacts {
		if matches(query, f.Name+" "+f.Body) {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		out = worldFacts
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SearchTranscript implements [tool.TranscriptSearcher].
func (w *world) SearchTranscript(_ context.Context, query string, limit int) ([]tool.TranscriptHit, error) {
	at := time.Date(2026, 11, 15, 20, 30, 0, 0, time.UTC)
	var out []tool.TranscriptHit
	for _, h := range worldLines {
		if matches(query, h.Who+" "+h.Text) {
			h.At = at
			out = append(out, h)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// OwnNode implements [tool.KGWriter].
func (w *world) OwnNode(context.Context, string) (tool.KGNodeRef, bool, error) {
	return tool.KGNodeRef{ID: nodeID, Name: npcName}, true, nil
}

// CreateProposal implements [tool.KGWriter]: the proposal is kept in memory.
func (w *world) CreateProposal(_ context.Context, _ string, p tool.ProposedWrite) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.proposals = append(w.proposals, p)
	return nil
}

// ExistingKnowledge implements [tool.KGWriter]: nothing is known yet, so no
// proposal is suppressed as a duplicate.
func (w *world) ExistingKnowledge(context.Context, string, tool.ProposedWrite) (tool.KnownForTarget, error) {
	return tool.KnownForTarget{}, nil
}

// matches reports whether any word of query (four letters or more) occurs in
// text, case-insensitively — a stand-in for the relevance search.
func matches(query, text string) bool {
	text = strings.ToLower(text)
	for _, word := range strings.Fields(strings.ToLower(query)) {
		word = strings.Trim(word, ".,;:!?\"'")
		if len(word) >= 4 && strings.Contains(text, word) {
			return true
		}
	}
	return false
}
