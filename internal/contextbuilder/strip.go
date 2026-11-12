package contextbuilder

import (
	"regexp"
	"strings"
)

// transcriptName matches one character of the name in a transcript prefix. A
// name never holds sentence punctuation, so "[12:30] 开会，记得带电脑：…" is
// real content.
const transcriptName = `[^\n:：，。！？、；,.!?;]`

// Annotations after the speaker's name in a history line: what it replies to
// and who it mentions, in that order.
const (
	replyAnnotationRE   = `（回复[^\n]{0,80}?）`
	mentionAnnotationRE = `（@了[^\n]{0,200}?）`
	annotationsRE       = `(?:` + replyAnnotationRE + `)?(?:` + mentionAnnotationRE + `)?`
)

// transcriptPrefixes match, at the start of a reply, the history line prefix
// "[HH:MM] 名字：" (optionally with its annotations), the header lines of the
// current message, the recent-history block and the current message's mention
// line. The memory block is cut by cutMemoryBlock.
var transcriptPrefixes = []*regexp.Regexp{
	regexp.MustCompile(`^\[\d{1,2}:\d{2}\]\s*` + transcriptName + `{1,40}?(?:` + replyAnnotationRE + `(?:` + mentionAnnotationRE + `)?|` + mentionAnnotationRE + `)\s*[：:]`),
	regexp.MustCompile(`^\[\d{1,2}:\d{2}\]\s*` + transcriptName + `{1,40}[：:]`),
	regexp.MustCompile(`^【当前消息】[^\n]*(?:\n|$)`),
	regexp.MustCompile(`^【近期消息[^\n]*(?:\n|$)`),
	regexp.MustCompile(`^` + mentionAnnotationRE + `[ \t]*(?:\n|$)`),
}

var (
	transcriptTime     = regexp.MustCompile(`^\[\d{1,2}:\d{2}\]\s*`)
	transcriptNameTail = regexp.MustCompile(`^` + annotationsRE + `(?:\s*[：:]|[ \t]*(?:\n|$))`)

	memoryBlockHeader = regexp.MustCompile(`^【你记得的事[^\n]*(?:\n|$)`)
	// memoryBlockLine matches a line of the memory block: the summary
	// "群里最近：…", a note "名字（档位）：…" or "名字：…" (a name without sentence
	// punctuation, not starting with "[" or "【"), or the closing reminder
	// starting "好感度只表示".
	memoryBlockLine = regexp.MustCompile(`^[ \t]*(?:群里最近[：:]|好感度只表示|[^\s\[【:：，。！？、；,.!?;]` +
		transcriptName + `{0,39}[：:])[^\n]*(?:\n|$)`)
)

// cutMemoryBlock removes an echoed memory block from the start of s: its
// header line and then every following line that reads like a block line,
// up to the first line that does not, so the notes go even when the closing
// line was reworded or left out.
func cutMemoryBlock(s string) (string, bool) {
	loc := memoryBlockHeader.FindStringIndex(s)
	if loc == nil {
		return s, false
	}
	s = s[loc[1]:]
	for {
		line := memoryBlockLine.FindStringIndex(s)
		if line == nil {
			return s, true
		}
		s = s[line[1]:]
	}
}

// StripTranscriptPrefix removes the prompt's transcript formatting that a
// model echoed at the start of its reply: repeated "[HH:MM] 名字：" prefixes
// (with or without their "（回复 …）" and "（@了 …）" annotations), the
// 【当前消息】 and 【近期消息…】 header lines, the 【你记得的事…】 header with the
// summary, note and closing lines after it (see cutMemoryBlock), a
// "（@了 …）" line and "<botName>：". A name holds no sentence punctuation
// unless it is one of knownNames (the conversation's speakers) or botName. For those names a bare
// header line "[HH:MM] 名字" without the colon (optionally with its
// annotations) is removed too. Text that does not start with such a prefix is
// returned unchanged.
func StripTranscriptPrefix(text, botName string, knownNames ...string) string {
	s := strings.TrimSpace(text)
	names := knownNames
	if botName != "" {
		names = append(knownNames[:len(knownNames):len(knownNames)], botName)
	}
	stripped := false
	for {
		cut := false
		for _, re := range transcriptPrefixes {
			if loc := re.FindStringIndex(s); loc != nil {
				s, cut = s[loc[1]:], true
			}
		}
		if rest, ok := cutMemoryBlock(s); ok {
			s, cut = rest, true
		}
		if rest, ok := cutKnownSpeaker(s, names); ok {
			s, cut = rest, true
		}
		if botName != "" {
			for _, sep := range []string{"：", ":"} {
				if rest, ok := strings.CutPrefix(s, botName+sep); ok {
					s, cut = rest, true
				}
			}
		}
		if !cut {
			break
		}
		s, stripped = strings.TrimSpace(s), true
	}
	if !stripped {
		return text
	}
	return s
}

// cutKnownSpeaker removes a "[HH:MM] <name>：" prefix, or a bare
// "[HH:MM] <name>" line ending in a newline or the end of s, optionally with
// its reply and mention annotations, whose name is exactly one of names,
// preferring the longest such name.
func cutKnownSpeaker(s string, names []string) (string, bool) {
	loc := transcriptTime.FindStringIndex(s)
	if loc == nil {
		return s, false
	}
	best, rest := "", ""
	for _, name := range names {
		after, ok := strings.CutPrefix(s[loc[1]:], name)
		if name == "" || !ok || len(name) <= len(best) {
			continue
		}
		if tail := transcriptNameTail.FindStringIndex(after); tail != nil {
			best, rest = name, after[tail[1]:]
		}
	}
	return rest, best != ""
}
