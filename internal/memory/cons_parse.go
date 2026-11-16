package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	consReasonMaxRunes = 200
	// consRawMaxRunes only guards against runaway output; storage limits are
	// applied by consResolveRefs after codes are replaced, so it stays well
	// above the largest limit and never cuts a code that is then replaced.
	consRawMaxRunes = 2 * conloomapi.MaxSummaryMaxChars
)

var errConsNoJSON = errors.New("model output has no JSON object")

// modelOutput is a parsed consolidation result. Members keeps every ref the
// model gave, known or not, in order.
type modelOutput struct {
	Summary string
	Members []modelMember
}

type modelMember struct {
	Ref, Note, Reason string
	Delta             int
}

// parseOutput reads the JSON object between the first "{" and the last "}"
// of content, so code fences and surrounding prose are ignored. The summary,
// notes and reasons are trimmed and cut only at consRawMaxRunes, a delta is rounded and
// limited to ±MaxRunDelta (0 when not a number), and only the first entry of
// a repeated ref is kept.
func parseOutput(content string) (modelOutput, error) {
	start, end := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if start < 0 || end < start {
		return modelOutput{}, errConsNoJSON
	}
	var raw struct {
		Summary string `json:"group_summary"`
		Members []struct {
			Ref    string `json:"ref"`
			Note   string `json:"note"`
			Delta  any    `json:"affinity_delta"`
			Reason string `json:"reason"`
		} `json:"members"`
	}
	if err := json.Unmarshal([]byte(content[start:end+1]), &raw); err != nil {
		return modelOutput{}, fmt.Errorf("model output: %w", err)
	}
	out := modelOutput{Summary: consTruncate(strings.TrimSpace(raw.Summary), consRawMaxRunes)}
	seen := map[string]bool{}
	for _, m := range raw.Members {
		ref := strings.TrimSpace(m.Ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		out.Members = append(out.Members, modelMember{
			Ref:    ref,
			Note:   consTruncate(strings.TrimSpace(m.Note), consRawMaxRunes),
			Reason: consTruncate(strings.TrimSpace(m.Reason), consRawMaxRunes),
			Delta:  consDelta(m.Delta),
		})
	}
	return out, nil
}

func consDelta(v any) int {
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return int(min(max(math.Round(f), -MaxRunDelta), MaxRunDelta))
}

// consTruncate cuts s to at most maxRunes runes: after the last sentence end
// (。！？；) within the limit when that keeps at least half of it, otherwise at
// the limit.
func consTruncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	r = r[:maxRunes]
	for i := len(r) - 1; 2*(i+1) >= maxRunes; i-- {
		if strings.ContainsRune("。！？；", r[i]) {
			return string(r[:i+1])
		}
	}
	return string(r)
}

// consTruncateNote cuts a note to at most maxRunes runes like consTruncate,
// but keeps the items its member asked the bot to remember, everything from
// the first RememberMarker on (see AppendRemembered): only the text before
// them is cut. When the items alone are over the limit, the oldest whole
// items are dropped, and a newest item that is still over it is cut at the
// limit.
func consTruncateNote(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	i := strings.Index(s, RememberMarker)
	if i < 0 {
		return consTruncate(s, maxRunes)
	}
	head, items := s[:i], s[i:]
	switch room := maxRunes - utf8.RuneCountInString(items); {
	case room > 0:
		return consTruncate(head, room) + items
	case room == 0:
		return items
	}
	for utf8.RuneCountInString(items) > maxRunes {
		next := strings.Index(items[len(RememberMarker):], RememberMarker)
		if next < 0 {
			return string([]rune(items)[:maxRunes])
		}
		items = items[len(RememberMarker)+next:]
	}
	return items
}
