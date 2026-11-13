package app

import (
	"fmt"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/skills/tools"
	"github.com/kinako404/conloom/internal/store"
)

// toolDeps are what the built-in tools need.
type toolDeps struct {
	Store    store.Store
	Policies *policy.Service
	Reviewer *review.Reviewer
	Metrics  *obs.Metrics
}

// builtinTools returns the built-in tools that skills may use. Every
// built-in tool is registered here.
func builtinTools(d toolDeps) []skills.Tool {
	return []skills.Tool{
		tools.NewRemember(tools.RememberDeps{Store: d.Store, Policies: d.Policies, Reviewer: d.Reviewer, Metrics: d.Metrics}),
		tools.NewRecall(tools.RecallDeps{Store: d.Store, Policies: d.Policies, Reviewer: d.Reviewer, IsCommand: isCommand,
			Metrics: d.Metrics}),
		tools.NewGetAffinity(tools.AffinityDeps{Store: d.Store, Policies: d.Policies}),
		tools.NewNow(),
	}
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
