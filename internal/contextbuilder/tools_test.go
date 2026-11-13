package contextbuilder

import (
	"reflect"
	"testing"

	"github.com/kinako404/conloom/internal/store"
)

func TestBuild_ToolSchemaCountsButIsNotRendered(t *testing.T) {
	in := baseInput()
	in.History = []store.MessageRecord{userMsg("小明", "在吗", at(12, 0)), botMsg("在", at(12, 1))}
	plain := Build(in)
	const schema = `[{"type":"function","function":{"name":"now","parameters":{"type":"object","properties":{}}}}]`
	in.ToolSchema = schema
	out := Build(in)
	if !reflect.DeepEqual(out.Messages, plain.Messages) {
		t.Errorf("messages changed: %+v", out.Messages)
	}
	cost := Estimate(schema, in.TokenFactor)
	if out.EstimatedTokens != plain.EstimatedTokens+cost {
		t.Errorf("EstimatedTokens = %d, want %d + %d", out.EstimatedTokens, plain.EstimatedTokens, cost)
	}
	plainIn := in
	plainIn.ToolSchema = ""
	if got, want := BaseTokens(in), BaseTokens(plainIn)+cost; got != want {
		t.Errorf("BaseTokens = %d, want %d", got, want)
	}

	// The schema takes budget from history.
	in.Budget = BaseTokens(in) + 2
	if out := Build(in); out.IncludedHistory != 0 {
		t.Errorf("IncludedHistory = %d, want 0 when the tools fill the budget", out.IncludedHistory)
	}
}
