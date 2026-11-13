package memory

import (
	"context"
	"regexp"
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
// conversation; Review is the conversation's review policy.
type RememberRequest struct {
	ConversationKey, UserID, DisplayName string
	Content                              string
	Review                               conloomapi.ReviewPolicy
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
	limit := NoteLimit(convTypeOf(req.ConversationKey))
	_, err := UpdateMember(ctx, d.Store, req.ConversationKey, req.UserID, store.RevisionCommand,
		func(m *store.MemberMemory) (string, bool) {
			m.Note = AppendRemembered(m.Note, content, limit)
			if req.DisplayName != "" && req.DisplayName != req.UserID {
				m.DisplayName = req.DisplayName
			}
			return ReasonRemember, true
		})
	if err != nil {
		return 0, err
	}
	return Remembered, nil
}
