package memory

import (
	"regexp"
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

var (
	// contactSeparators are dropped between digits before digit patterns
	// are matched, so "138 0000 1111" and "138-0000-1111" read as one number.
	contactSeparators = regexp.MustCompile(`(\d)[ \t\-.]+(\d)`)
	contactPatterns   = []*regexp.Regexp{
		regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+`),
		regexp.MustCompile(`\d{9,}`),
		regexp.MustCompile(`\d{17}[Xx]`),
		regexp.MustCompile(`\+\d{7,}`),
		regexp.MustCompile(`(?i)(qq|扣扣|企鹅号|微信|v信|vx|wx|wechat|weixin|电话|手机|座机|号码|tel|phone|身份证|证件|护照)` +
			`[^\p{Han}A-Za-z0-9]{0,3}(id|号码|号)?[^\p{Han}A-Za-z0-9]{0,3}(是|为)?[^\p{Han}A-Za-z0-9]{0,3}[A-Za-z0-9_+-]{5,}`),
	}
)

// LooksLikeContactOrID reports whether s seems to hold contact details or an
// identity number: an email address, a long run of digits (phone, QQ or ID
// card numbers), or a QQ, WeChat, phone or ID label followed by an
// identifier.
func LooksLikeContactOrID(s string) bool {
	compact := s
	for {
		next := contactSeparators.ReplaceAllString(compact, "$1$2")
		if next == compact {
			break
		}
		compact = next
	}
	for _, p := range contactPatterns {
		if p.MatchString(compact) {
			return true
		}
	}
	return false
}

// WithoutRemembered is note without the items its member asked the bot to
// remember: everything from the first RememberMarker on, for showing the
// note to anyone but that member.
func WithoutRemembered(note string) string {
	before, _, _ := strings.Cut(note, RememberMarker)
	return strings.TrimSpace(before)
}
