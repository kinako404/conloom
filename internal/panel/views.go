package panel

import (
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// nullTime is t in UTC, or nil when t is zero.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

// modelCallView is a model call as usage/calls and task detail list it.
type modelCallView struct {
	ID               int64     `json:"id"`
	At               time.Time `json:"at"`
	BotInstanceID    string    `json:"bot_instance_id"`
	ConversationKey  string    `json:"conversation_key"`
	TaskID           string    `json:"task_id"`
	Purpose          string    `json:"purpose"`
	Role             string    `json:"role"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	ReasoningTokens  int       `json:"reasoning_tokens"`
	EstimatedTokens  int       `json:"estimated_tokens"`
	LatencyMS        int64     `json:"latency_ms"`
	Outcome          string    `json:"outcome"`
	FinishReason     string    `json:"finish_reason"`
}

func newModelCallView(c store.ModelCall) modelCallView {
	return modelCallView{
		ID: c.ID, At: c.At.UTC(), BotInstanceID: c.BotInstanceID, ConversationKey: c.ConversationKey, TaskID: c.TaskID,
		Purpose: c.Purpose, Role: c.Role, Model: c.Model, PromptTokens: c.PromptTokens,
		CompletionTokens: c.CompletionTokens, ReasoningTokens: c.ReasoningTokens, EstimatedTokens: c.EstimatedTokens,
		LatencyMS: c.LatencyMS, Outcome: c.Outcome, FinishReason: c.FinishReason,
	}
}

// policyVersionView is a policy version; CreatedAt is null for the defaults
// of a conversation without a stored policy (version 0).
type policyVersionView struct {
	Version   int                    `json:"version"`
	Policy    conloomapi.GroupPolicy `json:"policy"`
	Reason    string                 `json:"reason"`
	UpdatedBy string                 `json:"updated_by"`
	CreatedAt *time.Time             `json:"created_at"`
}

func newPolicyVersionView(pv store.PolicyVersion) policyVersionView {
	return policyVersionView{
		Version: pv.Version, Policy: pv.Policy, Reason: pv.Reason, UpdatedBy: pv.UpdatedBy, CreatedAt: nullTime(pv.CreatedAt),
	}
}

// personaVersionView is a persona version. Every stored version has a
// creation time; a zero one would be null as in policyVersionView.
type personaVersionView struct {
	Version   int                   `json:"version"`
	Template  string                `json:"template"`
	Persona   conloomapi.PersonaDoc `json:"persona"`
	Reason    string                `json:"reason"`
	UpdatedBy string                `json:"updated_by"`
	CreatedAt *time.Time            `json:"created_at"`
}

func newPersonaVersionView(pv store.PersonaVersion) personaVersionView {
	return personaVersionView{
		Version: pv.Version, Template: pv.TemplateName, Persona: pv.Persona, Reason: pv.Reason,
		UpdatedBy: pv.UpdatedBy, CreatedAt: nullTime(pv.CreatedAt),
	}
}
