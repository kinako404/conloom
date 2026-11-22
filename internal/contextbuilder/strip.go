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

	// stateBlockHeader matches the heading of the state block, 【你的状态…】,
	// or of its outer section, 【外在状态…】.
	stateBlockHeader = regexp.MustCompile(`^【(?:你的状态|外在状态)[^\n]*(?:\n|$)`)
	// stateBlockLine matches a line of the state block: the scene
	// "场景：…", the status "正在：…", an inner bar "名字：档位…" (any bar
	// name, which holds no whitespace, 【】 or colon, followed by one of the
	// five bands, with or without its step limit), the effects "效果：…", the
	// closing reminder starting "这些只影响", the outer heading 【外在状态…】
	// or an outer bar "名字：80/100…". Other "短词：…" lines are kept.
	stateBlockLine = blockLine(`场景[：:]|正在[：:]|效果[：:]|这些只影响|【外在状态|` +
		`[^\s【】\n:：]{1,6}[：:](?:很低|偏低|中等|偏高|很高)|[^\s【】\n:：]{1,6}[：:]\d{1,3}/100`)
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
// 【你的状态…】 header with the scene, status, bar, effects and closing lines,
// the 【外在状态…】 heading and the outer bar lines "名字：80/100…" after it,
// or a 【外在状态…】 heading alone with the outer bar lines after it (see
// cutBlock), a "（@了 …）" line and "<botName>：". The 语气, 心理 and
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

var (
	// seenImageText matches the whole text of a seen current image, [图片n].
	seenImageText = regexp.MustCompile(`^\[图片\d+\]$`)
	// imageLabelEchoes matches a run of prompt image markers a model echoed,
	// with the blanks around it: a ChatImageLabel (in this or its earlier
	// wording), [图片n], imageSeenEarlierPlaceholder, imageUnseenPlaceholder
	// and imageOnlyNote.
	imageLabelEchoes = regexp.MustCompile(`[ \t\x{3000}]*(?:` + strings.Join([]string{
		`【对方[^【】\n]{0,20}第 ?\d{1,2} ?张图片[^【】\n]{0,40}】`,
		`\[图片\d{1,2}\]`,
		regexp.QuoteMeta(imageSeenEarlierPlaceholder),
		regexp.QuoteMeta(imageUnseenPlaceholder),
		regexp.QuoteMeta(imageOnlyNote),
	}, "|") + `)+[ \t\x{3000}]*`)
)

// StripImageLabels removes the prompt's image markers that a model echoed
// anywhere in its reply (see imageLabelEchoes). A marker between two blanks
// leaves one space; other blanks next to it go. A line left blank is dropped,
// with the blank lines that would then double up, and the result is trimmed.
// Text without such a marker, including other bracketed text like [笑] or
// [图片：…], is returned unchanged.
func StripImageLabels(text string) string {
	if !imageLabelEchoes.MatchString(text) {
		return text
	}
	var out []string
	dropped := false
	for line := range strings.SplitSeq(text, "\n") {
		if imageLabelEchoes.MatchString(line) {
			line = imageLabelEchoes.ReplaceAllStringFunc(line, func(m string) string {
				if strings.TrimLeft(m, " \t\u3000") != m && strings.TrimRight(m, " \t\u3000") != m {
					return " "
				}
				return ""
			})
			if strings.TrimSpace(line) == "" {
				dropped = true
				continue
			}
			line = strings.TrimRight(line, " \t\u3000")
		} else if dropped && strings.TrimSpace(line) == "" && (len(out) == 0 || strings.TrimSpace(out[len(out)-1]) == "") {
			continue
		}
		dropped = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
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
