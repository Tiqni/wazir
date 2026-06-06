package orchestrator

import (
	"strings"

	"github.com/EmadMokhtar/wazir/internal/board"
)

// BuildTranscript renders a card's title, body, and thread as a single
// transcript the Brain can reason over. Each comment is tagged HUMAN: or
// SYSTEM: by IsBot (init-plan §8.3).
func BuildTranscript(c board.Card) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(c.Title)
	b.WriteString("\n\n")
	b.WriteString(c.Body)
	b.WriteString("\n")
	for _, cm := range c.Comments {
		tag := "HUMAN:"
		if cm.IsBot {
			tag = "SYSTEM:"
		}
		b.WriteString("\n")
		b.WriteString(tag)
		b.WriteString(" ")
		b.WriteString(cm.Body)
		b.WriteString("\n")
	}
	return b.String()
}
