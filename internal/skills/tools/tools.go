// Package tools holds the built-in tools that skills expose to the model:
// remember, recall, get_affinity and now. Each acts only for the speaker and
// conversation of its skills.Invocation, never for anyone named in its
// arguments.
package tools

import (
	"context"
	"encoding/json"

	"github.com/kinako404/conloom/internal/store"
)

// PolicySource gives a conversation's current policy; *policy.Service is one.
type PolicySource interface {
	Current(ctx context.Context, convKey string) (store.PolicyVersion, error)
}

// noArguments is the schema of tools that take no arguments.
var noArguments = json.RawMessage(`{"type":"object","properties":{}}`)

// memoryOn reports whether memory is in effect for convKey, as for the
// memory commands: enabled, with long requests allowed.
func memoryOn(ctx context.Context, policies PolicySource, convKey string) (store.PolicyVersion, bool, error) {
	pv, err := policies.Current(ctx, convKey)
	if err != nil {
		return store.PolicyVersion{}, false, err
	}
	p := pv.Policy
	return pv, p.Memory.Enabled && p.Concurrency.AllowLongRequests, nil
}

// stringArg returns the string argument name of args, a JSON object; ok is
// false when args is not an object or name is missing or not a string.
func stringArg(args json.RawMessage, name string) (value string, ok bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return "", false
	}
	raw, found := obj[name]
	if !found {
		return "", false
	}
	if err := json.Unmarshal(raw, &value); err != nil || string(raw) == "null" {
		return "", false
	}
	return value, true
}
