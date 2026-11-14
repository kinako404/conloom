package memory

import (
	"cmp"
	"context"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// RememberMarker starts each item a member asked the bot to remember.
	RememberMarker = "（他让我记住）"
	// RememberMaxRunes bounds what one /记住 adds.
	RememberMaxRunes = 100
	// ReasonRemember is the reason of revisions written by /记住.
	ReasonRemember = "remember"
	// ReasonRememberTool is the reason of revisions written by the remember
	// tool.
	ReasonRememberTool = "remember_tool"
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

// contactPattern finds one kind of contact detail. A pattern that reads
// digits separated by spaces, hyphens or dots as one number (grouped) only
// counts when the match is not continued by further digits (digitBounded), so
// dates and times such as "2026-11-20 10点" or "2026 11 13 10 30" are not
// taken for numbers; for the label pattern this applies only when its
// identifier is such groups (submatch groupedSub).
type contactPattern struct {
	re         *regexp.Regexp
	grouped    bool
	groupedSub int
}

var contactPatterns = []contactPattern{
	// Email.
	{re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+`)},
	// An unbroken run of digits: phone, QQ or identity numbers.
	{re: regexp.MustCompile(`\d{9,}`)},
	{re: regexp.MustCompile(`\d{17}[Xx]`)},
	// Identity card numbers in groups: six digits, then eleven more and the
	// check character.
	{re: regexp.MustCompile(`\d{6}(?:[ .-]?\d){11}[\dXx]`), grouped: true},
	// Mobile numbers in any grouping, such as 3-4-4, 4-4-4 or 3-8, with an
	// optional extension after a hyphen.
	{re: regexp.MustCompile(`(?:1[3-9]\d{2}[ .-]\d{4}[ .-]\d{4}|1[3-9]\d(?:[ .-]?\d){8})(?:-\d{1,4})?`), grouped: true},
	// Landlines with an area code: 010 12345678, 021-1234-5678.
	{re: regexp.MustCompile(`0\d{2,3}[ -](?:\d{7,8}|\d{3,4}[ -]\d{4})`), grouped: true},
	// International numbers.
	{re: regexp.MustCompile(`\+\d{1,3}(?:[ .-]?\d{2,4}){2,}`), grouped: true},
	// A label followed by an identifier, which may be digit groups.
	{re: regexp.MustCompile(`(?i)(qq|扣扣|企鹅号|微信|v信|vx|wx|wechat|weixin|电话|手机|座机|号码|tel|phone|身份证|证件|护照)` +
		`[^\p{Han}A-Za-z0-9]{0,3}(id|号码|号)?[^\p{Han}A-Za-z0-9]{0,3}(是|为)?[^\p{Han}A-Za-z0-9]{0,3}` +
		`(?:[A-Za-z0-9_+-]{5,}|(\+?\d{2,}(?:[ .-]\d{2,})+))`), groupedSub: 4},
}

// ContactPlaceholder replaces contact details and identity numbers in
// RedactContactOrID.
const ContactPlaceholder = "［联系方式］"

// LooksLikeContactOrID reports whether s seems to hold contact details or an
// identity number: an email address, a long run of digits (phone, QQ or ID
// card numbers), or a QQ, WeChat, phone or ID label followed by an
// identifier.
func LooksLikeContactOrID(s string) bool {
	return len(contactSpans(s)) > 0
}

// RedactContactOrID replaces each part of s that LooksLikeContactOrID would
// find with ContactPlaceholder.
func RedactContactOrID(s string) string {
	spans := contactSpans(s)
	if len(spans) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(s[last:sp[0]])
		b.WriteString(ContactPlaceholder)
		last = sp[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// contactSpans returns the sorted, disjoint byte ranges of s matched by
// contactPatterns, leaving out grouped matches that are part of a longer
// digit sequence (such as a date continuing with more numbers).
func contactSpans(s string) [][2]int {
	var spans [][2]int
	for _, p := range contactPatterns {
		for _, m := range p.re.FindAllStringSubmatchIndex(s, -1) {
			grouped := p.grouped || (p.groupedSub > 0 && m[2*p.groupedSub] >= 0)
			if !grouped || digitBounded(s, m[0], m[1]) {
				spans = append(spans, [2]int{m[0], m[1]})
			}
		}
	}
	slices.SortFunc(spans, func(a, b [2]int) int { return cmp.Compare(a[0], b[0]) })
	var merged [][2]int
	for _, sp := range spans {
		if n := len(merged); n > 0 && sp[0] <= merged[n-1][1] {
			merged[n-1][1] = max(merged[n-1][1], sp[1])
			continue
		}
		merged = append(merged, sp)
	}
	return merged
}

// digitBounded reports whether s[start:end] is not continued by digits on
// either side, directly or across one separator (space, hyphen, dot, slash or
// colon): "1380000111122" or "2026-13800001111" hold no phone number of their
// own.
func digitBounded(s string, start, end int) bool {
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	isSep := func(c byte) bool { return strings.IndexByte(" -./:", c) >= 0 }
	if start > 0 && isDigit(s[start-1]) && isDigit(s[start]) {
		return false
	}
	if start > 1 && isSep(s[start-1]) && isDigit(s[start-2]) && isDigit(s[start]) {
		return false
	}
	if end < len(s) && isDigit(s[end]) && isDigit(s[end-1]) {
		return false
	}
	if end+1 < len(s) && isSep(s[end]) && isDigit(s[end+1]) && isDigit(s[end-1]) {
		return false
	}
	return true
}

// WithoutRemembered is note without the items its member asked the bot to
// remember: everything from the first RememberMarker on, for showing the
// note to anyone but that member.
func WithoutRemembered(note string) string {
	before, _, _ := strings.Cut(note, RememberMarker)
	return strings.TrimSpace(before)
}

// RememberStatus is the outcome of Remember.
type RememberStatus int

const (
	Remembered        RememberStatus = iota
	RememberEmpty                    // nothing left after trimming
	RememberTooLong                  // over RememberMaxRunes
	RememberSensitive                // contact details or an identity number
	RememberBlocked                  // refused by review
)

// Reviewer is the part of review.Reviewer that Remember uses.
type Reviewer interface {
	Input(text string, pol conloomapi.ReviewPolicy) review.Result
	Output(text string, pol conloomapi.ReviewPolicy) review.Result
}

// RememberDeps are what Remember needs. Metrics may be nil.
type RememberDeps struct {
	Store    MemberStore
	Reviewer Reviewer
	Metrics  *obs.Metrics
}

// RememberRequest is one thing a member asked the bot to remember in a
// conversation; Review and Memory are the conversation's review and memory
// policies (Memory sets the note limit, see NoteLimit) and Reason the
// revision's reason, ReasonRemember when empty.
type RememberRequest struct {
	ConversationKey, UserID, DisplayName string
	Content                              string
	Review                               conloomapi.ReviewPolicy
	Memory                               conloomapi.MemoryPolicy
	Reason                               string
}

// Remember appends what a member asked to remember to their note (see
// AppendRemembered) within the conversation's note limit, with a note
// revision from the command source. The content has its whitespace collapsed
// and must be 1..RememberMaxRunes runes; contact details and identity numbers
// are refused, and the rest goes through the review of incoming messages and
// then the redaction and blocked words of outgoing ones, as it may be shown to
// the reply model. The caller checks that memory is on.
func Remember(ctx context.Context, d RememberDeps, req RememberRequest) (RememberStatus, error) {
	content := strings.Join(strings.Fields(req.Content), " ")
	switch n := utf8.RuneCountInString(content); {
	case n == 0:
		return RememberEmpty, nil
	case n > RememberMaxRunes:
		return RememberTooLong, nil
	case LooksLikeContactOrID(content):
		return RememberSensitive, nil
	}
	in := d.Reviewer.Input(content, req.Review)
	out := d.Reviewer.Output(in.Text, req.Review)
	if d.Metrics != nil {
		d.Metrics.Review.WithLabelValues("input", in.Verdict).Inc()
		d.Metrics.Review.WithLabelValues("output", out.Verdict).Inc()
	}
	if content = strings.Join(strings.Fields(out.Text), " "); out.Verdict == review.VerdictBlock || content == "" {
		return RememberBlocked, nil
	}
	reason := cmp.Or(req.Reason, ReasonRemember)
	limit := NoteLimit(req.Memory, convTypeOf(req.ConversationKey))
	_, err := UpdateMember(ctx, d.Store, req.ConversationKey, req.UserID, store.RevisionCommand,
		func(m *store.MemberMemory) (string, bool) {
			m.Note = AppendRemembered(m.Note, content, limit)
			if req.DisplayName != "" && req.DisplayName != req.UserID {
				m.DisplayName = req.DisplayName
			}
			return reason, true
		})
	if err != nil {
		return 0, err
	}
	return Remembered, nil
}
