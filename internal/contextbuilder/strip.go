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

// transcriptPrefixes match, at the start of a reply, the appearance label
// line, the history line prefix "[HH:MM] 名字：" (optionally with its
// annotations), the header lines of the current message, the recent-history
// block, the relationship hint line and the current message's mention line.
// The memory, intimacy side and state blocks are cut by cutBlock.
var transcriptPrefixes = []*regexp.Regexp{
	regexp.MustCompile(`^` + regexp.QuoteMeta(AppearanceLabel) + `[ \t]*(?:\n|$)`),
	regexp.MustCompile(`^\[\d{1,2}:\d{2}\]\s*` + transcriptName + `{1,40}?(?:` + replyAnnotationRE + `(?:` + mentionAnnotationRE + `)?|` + mentionAnnotationRE + `)\s*[：:]`),
	regexp.MustCompile(`^\[\d{1,2}:\d{2}\]\s*` + transcriptName + `{1,40}[：:]`),
	regexp.MustCompile(`^【当前消息】[^\n]*(?:\n|$)`),
	regexp.MustCompile(`^【近期消息[^\n]*(?:\n|$)`),
	regexp.MustCompile(`^【关系提示】[^\n]*(?:\n|$)`),
	regexp.MustCompile(`^` + mentionAnnotationRE + `[ \t]*(?:\n|$)`),
}

var (
	transcriptTime     = regexp.MustCompile(`^\[\d{1,2}:\d{2}\]\s*`)
	transcriptNameTail = regexp.MustCompile(`^` + annotationsRE + `(?:\s*[：:]|[ \t]*(?:\n|$))`)

	memoryBlockHeader = regexp.MustCompile(`^【你记得的事[^\n]*(?:\n|$)`)
	memoryBlockLine   = blockLine(memoryLineStart)

	// The intimacy side that follows the relationship hint: its 【亲密面】
	// heading, then a line each for tone, mind and behavior. These lines are
	// cut only right after the heading.
	intimacyBlockHeader = regexp.MustCompile(`^【亲密面】[^\n]*(?:\n|$)`)
	intimacyBlockLine   = blockLine(`语气[：:]|心理[：:]|表现[：:]`)

	stateBlockHeader = regexp.MustCompile(`^【你的状态[^\n]*(?:\n|$)`)
	// stateBlockLine matches a line of the state block: the scene
	// "场景：…", the status "正在：…", a bar "名字：档位…" (any bar name,
	// which holds no whitespace, 【】 or colon, followed by one of the five
	// bands), the effects "效果：…" or the closing reminder starting
	// "这些只影响". Other "短词：…" lines are kept.
	stateBlockLine = blockLine(`场景[：:]|正在[：:]|效果[：:]|这些只影响|[^\s【】\n:：]{1,6}[：:](?:很低|偏低|中等|偏高|很高)`)
)

// memoryLineStart matches the start of a memory block line: the summary
// "群里最近：…" or "你们最近聊过：…", a note "名字（档位）：…" or "名字：…" (a
// name without sentence punctuation, not starting with "[" or "【"), or the
// closing reminder starting "好感度只表示".
const memoryLineStart = `群里最近[：:]|你们最近聊过[：:]|好感度只表示|[^\s\[【:：，。！？、；,.!?;]` +
	transcriptName + `{0,39}[：:]`

// blockLine matches a whole line, optionally indented, that starts with one of
// the alternatives in starts.
func blockLine(starts string) *regexp.Regexp {
	return regexp.MustCompile(`^[ \t]*(?:` + starts + `)[^\n]*(?:\n|$)`)
}

// cutBlock removes an echoed prompt block from the start of s: its header
// line and then every following line that reads like a block line, up to the
// first line that does not, so the block's lines go even when its closing
// line was reworded or left out.
func cutBlock(s string, header, line *regexp.Regexp) (string, bool) {
	loc := header.FindStringIndex(s)
	if loc == nil {
		return s, false
	}
	s = s[loc[1]:]
	for {
		next := line.FindStringIndex(s)
		if next == nil {
			return s, true
		}
		s = s[next[1]:]
	}
}

// StripTranscriptPrefix removes the prompt's transcript formatting that a
// model echoed at the start of its reply: the AppearanceLabel line, repeated
// "[HH:MM] 名字：" prefixes
// (with or without their "（回复 …）" and "（@了 …）" annotations), the
// 【当前消息】 and 【近期消息…】 header lines, the 【你记得的事…】 header with the
// summary, note and closing lines after it, the 【关系提示】 line, the 【亲密面】
// heading with the "语气：…", "心理：…" and "表现：…" lines after it, the
// 【你的状态…】 header with the scene, status, bar, effects and closing lines
// after it (see cutBlock), a "（@了 …）" line and "<botName>：". The 语气, 心理 and
// 表现 lines are kept when they do not follow a 【亲密面】 heading. A name holds no sentence punctuation
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
		if rest, ok := cutBlock(s, memoryBlockHeader, memoryBlockLine); ok {
			s, cut = rest, true
		}
		if rest, ok := cutBlock(s, intimacyBlockHeader, intimacyBlockLine); ok {
			s, cut = rest, true
		}
		if rest, ok := cutBlock(s, stateBlockHeader, stateBlockLine); ok {
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
