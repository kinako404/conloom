package memory

import (
	"strings"
	"unicode/utf8"
)

const (
	// RememberMarker starts each item a member asked the bot to remember.
	RememberMarker = "（他让我记住）"
	// RememberMaxRunes bounds what one /记住 adds.
	RememberMaxRunes = 100
	// ReasonRemember is the reason of revisions written by /记住.
	ReasonRemember = "remember"
)

// AppendRemembered appends RememberMarker and content to note. When the
// result would exceed maxRunes, the oldest part of note is dropped: whole
// sentences or remembered items where a boundary allows, otherwise exactly
// enough runes.
func AppendRemembered(note, content string, maxRunes int) string {
	entry := RememberMarker + content
	room := maxRunes - utf8.RuneCountInString(entry)
	old := []rune(note)
	if len(old) <= room {
		return note + entry
	}
	if room <= 0 {
		return string([]rune(entry)[:maxRunes])
	}
	cut := len(old) - room
	marker := []rune(RememberMarker)
	for i := cut; i <= len(old); i++ {
		atSentence := i > 0 && strings.ContainsRune("。！？；", old[i-1])
		atMarker := i+len(marker) <= len(old) && string(old[i:i+len(marker)]) == RememberMarker
		if atSentence || atMarker {
			return strings.TrimSpace(string(old[i:])) + entry
		}
	}
	return string(old[cut:]) + entry
}
