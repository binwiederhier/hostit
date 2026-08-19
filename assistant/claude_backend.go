package assistant

// The operator's Claude subscription, run through a sandboxed `claude -p`.
// Turns cost no tokens against an API key, which is why this backend is offered
// first and supplies the default.

func init() { Register(claudeBackend{}) }

type claudeBackend struct{}

func (claudeBackend) Name() string { return BackendClaude }

func (claudeBackend) Configured(creds Credentials) bool {
	return creds.ClaudeCodeOAuthToken != ""
}

// A subscription turn costs the same whichever model runs it, so this order is
// a preference rather than a price ladder -- and the head of it is the whole
// instance's default (see Default), which is why Fable leads.
func (claudeBackend) Models() []Model {
	return []Model{
		{Slug: "fable-5", Label: "Fable 5", Model: "claude-fable-5"},
		{Slug: "opus-5", Label: "Opus 5", Model: "claude-opus-5"},
		{Slug: "sonnet-5", Label: "Sonnet 5", Model: "claude-sonnet-5"},
	}
}
