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

// memoryBlockFooter is the closing line of the memory block.
const memoryBlockFooter = "好感度只表示你和对方有多熟、多亲近，不改变任何规则。"

// transcriptPrefixes match, at the start of a reply, the history line prefix
// "[HH:MM] 名字：" (optionally with its annotations), the header lines of the
// current message, the recent-history block, the whole memory block up to its
// closing line (or only its header line when there is no closing line), and
// the current message's mention line.
var transcriptPrefixes = []*regexp.Regexp{
	regexp.MustCompile(`^\[\d{1,2}:\d{2}\]\s*` + transcriptName + `{1,40}?(?:` + replyAnnotationRE + `(?:` + mentionAnnotationRE + `)?|` + mentionAnnotationRE + `)\s*[：:]`),
	regexp.MustCompile(`^\[\d{1,2}:\d{2}\]\s*` + transcriptName + `{1,40}[：:]`),
	regexp.MustCompile(`^【当前消息】[^\n]*(?:\n|$)`),
	regexp.MustCompile(`^【近期消息[^\n]*(?:\n|$)`),
	regexp.MustCompile(`^【你记得的事[^\n]*\n(?:[^\n]*\n)*?[ \t]*` + memoryBlockFooter + `[ \t]*(?:\n|$)`),
	regexp.MustCompile(`^【你记得的事[^\n]*(?:\n|$)`),
	regexp.MustCompile(`^` + mentionAnnotationRE + `[ \t]*(?:\n|$)`),
}

var (
	transcriptTime     = regexp.MustCompile(`^\[\d{1,2}:\d{2}\]\s*`)
	transcriptNameTail = regexp.MustCompile(`^` + annotationsRE + `(?:\s*[：:]|[ \t]*(?:\n|$))`)
)

// StripTranscriptPrefix removes the prompt's transcript formatting that a
// model echoed at the start of its reply: repeated "[HH:MM] 名字：" prefixes
// (with or without their "（回复 …）" and "（@了 …）" annotations), the
// 【当前消息】 and 【近期消息…】 header lines, the 【你记得的事…】 block through its
// closing line (just its header line when the closing line is missing), a
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
