package app

import (
	"fmt"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/skills"
)

// builtinTools returns the built-in tools that skills may use. Every
// built-in tool is registered here.
func builtinTools() []skills.Tool {
	return nil
}

// loadSkills loads the built-in skills and those in skills.dir, registers
// tools and checks the skills each bot enables. Every error wraps
// skills.ErrInvalid.
func loadSkills(cfg *config.Config, tools []skills.Tool) (*skills.Registry, error) {
	loaded, err := skills.Load(cfg.Skills.Dir)
	if err != nil {
		return nil, err
	}
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
