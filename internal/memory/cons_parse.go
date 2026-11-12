package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

const consReasonMaxRunes = 200

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
// notes and reasons are cut to their rune limits, a delta is rounded and
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
	out := modelOutput{Summary: consTruncate(strings.TrimSpace(raw.Summary), SummaryMaxRunes)}
	seen := map[string]bool{}
	for _, m := range raw.Members {
		ref := strings.TrimSpace(m.Ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		out.Members = append(out.Members, modelMember{
			Ref:    ref,
			Note:   consTruncate(strings.TrimSpace(m.Note), NoteMaxRunes),
			Reason: consTruncate(strings.TrimSpace(m.Reason), consReasonMaxRunes),
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

func consTruncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}
