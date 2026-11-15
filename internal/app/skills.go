package app

import (
	"context"
	"fmt"

	"github.com/kinako404/conloom/internal/botdir"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/skills/tools"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// toolDeps are what the built-in tools need.
type toolDeps struct {
	Store    store.Store
	Policies *policy.Service
	Personas tools.PersonaSource
	Reviewer *review.Reviewer
	Metrics  *obs.Metrics
	// Vectors is semantic search for recall, nil without an embedding
	// section in the config.
	Vectors search.VectorSearcher
}

// builtinTools returns the built-in tools that skills may use. Every
// built-in tool is registered here.
func builtinTools(d toolDeps) []skills.Tool {
	return []skills.Tool{
		tools.NewRemember(tools.RememberDeps{Store: d.Store, Policies: d.Policies, Reviewer: d.Reviewer, Metrics: d.Metrics}),
		tools.NewRecall(tools.RecallDeps{Store: d.Store, Policies: d.Policies, Reviewer: d.Reviewer, IsCommand: isCommand,
			Vectors: d.Vectors, Metrics: d.Metrics}),
		tools.NewGetAffinity(tools.AffinityDeps{Store: d.Store, Policies: d.Policies}),
		tools.NewNow(),
		tools.NewSetStatus(d.Store, d.Policies, d.Personas, d.Reviewer),
		tools.NewAdjustBuff(d.Store, d.Personas),
	}
}

// personaSource is the tools.PersonaSource of the app: a conversation's
// current persona version, created from its bot's persona template when it
// has none, as the core does.
type personaSource struct {
	bots     *botdir.Directory
	personas *persona.Service
}

func (p personaSource) Persona(ctx context.Context, botInstanceID, convKey string) (conloomapi.PersonaDoc, error) {
	bot, ok := p.bots.Bot(botInstanceID)
	if !ok {
		return conloomapi.PersonaDoc{}, fmt.Errorf("app: unknown bot %q", botInstanceID)
	}
	pv, err := p.personas.Current(ctx, convKey, bot.PersonaTemplate)
	if err != nil {
		return conloomapi.PersonaDoc{}, err
	}
	return pv.Persona, nil
}

// isCommand reports whether text is a command under prefix, as the core
// recognises commands.
func isCommand(text, prefix string) bool {
	_, _, ok := core.ParseCommand(text, prefix)
	return ok
}

// registerSkills registers tools for the loaded skills and checks the skills
// each bot enables. Every error wraps skills.ErrInvalid.
func registerSkills(cfg *config.Config, loaded map[string]skills.Skill, tools []skills.Tool) (*skills.Registry, error) {
	reg, err := skills.NewRegistry(loaded, tools)
	if err != nil {
		return nil, err
	}
	for i, b := range cfg.Bots {
		if err := reg.Check(b.Skills); err != nil {
			return nil, fmt.Errorf("bots[%d].skills: %w", i, err)
		}
	}
	return reg, nil
}
