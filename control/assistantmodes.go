package control

import (
	"heckel.io/hostit/assistant"
	"heckel.io/hostit/control/config"
	"heckel.io/hostit/store"
)

// The assistant's mode surface. Everything here is derived: which options exist
// follows from which credentials are configured (assistant.Catalog), and which
// one a turn runs follows from what the app last chose. There is no operator
// catalog to keep in sync and no per-user allowlist -- an instance approves its
// signups, and that is the control.

// credentials is what the assistant needs from the config to build its catalog.
// The config package cannot import the assistant (the sandbox imports the
// config), so the server bridges the two.
func credentials(conf *config.Config) assistant.Credentials {
	return assistant.Credentials{
		AnthropicAPIKey:      conf.AnthropicAPIKey,
		ClaudeCodeOAuthToken: conf.ClaudeCodeOAuthToken,
	}
}

// assistantOptions is every mode this instance can run, in catalog order: the
// subscription's models first, then the metered API's, each carrying the backend
// that runs it so the UI can group and label them.
func (s *Server) assistantOptions() []assistant.Option {
	return assistant.Catalog(credentials(s.config))
}

// resolveMode picks the mode a turn actually runs: the requested one if this
// instance can run it, else the app's remembered choice, else the default. An
// empty answer means nothing is configured, which is also when the assistant
// hides itself.
//
// A remembered choice that names a backend no longer configured simply does not
// resolve, so removing a credential downgrades an app's next turn instead of
// failing it.
func (s *Server) resolveMode(requested, appName string) string {
	creds := credentials(s.config)
	if o, ok := assistant.Lookup(creds, requested); ok {
		return o.ID
	}
	if saved, err := s.apps.Store().AppAssistantMode(appName); err == nil {
		if o, ok := assistant.Lookup(creds, saved); ok {
			return o.ID
		}
	}
	// The instance default (control.yml, admin-overridable) if it names a mode
	// this instance can actually run; a stale/unconfigured id is ignored.
	if o, ok := assistant.Lookup(creds, s.defaultAssistantModel()); ok {
		return o.ID
	}
	if o, ok := assistant.Default(creds); ok {
		return o.ID
	}
	return ""
}

// defaultAssistantModel is the effective instance default mode id: the admin-set
// DB value if present, else the control.yml default. It is only advisory --
// resolveMode ignores an id no configured backend serves.
func (s *Server) defaultAssistantModel() string {
	if v, err := s.apps.Store().Setting(store.SettingDefaultAssistantModel); err == nil && v != "" {
		return v
	}
	return s.config.DefaultAssistantModel
}
