package control

import (
	"fmt"
	"strings"

	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/store"
)

// boolStr renders a bool as the "1"/"0" the settings table stores.
func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// errInvalidMode reports a mode that is neither External Claude nor a known model.
func errInvalidMode(mode string) error {
	return fmt.Errorf("invalid assistant mode %q", mode)
}

// apiAssistantMode is one selectable option in the chat's mode dropdown.
type apiAssistantMode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// apiAssistantDefaults is the operator's global assistant defaults plus the API
// model catalog, for the admin UI.
type apiAssistantDefaults struct {
	ExternalAllowed    bool               `json:"external_allowed"`
	AllowedModels      []string           `json:"allowed_models"` // empty = all models
	DefaultMode        string             `json:"default_mode"`
	Models             []apiAssistantMode `json:"models"`              // API models (no External Claude)
	ExternalConfigured bool               `json:"external_configured"` // is the subscription set up
}

// assistantDefaults builds the global assistant defaults block for the settings
// response: the operator's defaults plus the API model catalog for the admin UI.
// The assistant defaults live inside /api/settings, not on a separate endpoint.
func (s *Server) assistantDefaults(settings map[string]string) *apiAssistantDefaults {
	return &apiAssistantDefaults{
		ExternalAllowed:    s.defaultExternalAllowed(settings),
		AllowedModels:      splitCSV(settings[store.SettingAssistantDefaultModels]),
		DefaultMode:        s.defaultMode(),
		Models:             modelCatalog(s.config),
		ExternalConfigured: s.config.ClaudeBackendEnabled(),
	}
}

// modelCatalog is the configured API models as dropdown options (no External Claude).
func modelCatalog(conf *controlconf.Config) []apiAssistantMode {
	out := make([]apiAssistantMode, 0, len(conf.AssistantModels))
	for _, m := range conf.AssistantModels {
		out = append(out, apiAssistantMode{ID: m.ID, Label: m.Label})
	}
	return out
}

// assistantOptions returns the modes a given user may pick: External Claude (only
// if the subscription is configured AND the user is permitted) plus the API models
// their allowlist permits. The admin token (empty userID) gets everything.
func (s *Server) assistantOptions(userID string) []apiAssistantMode {
	externalAllowed, allowedModels := s.effectiveUserAssistant(userID)
	allowedSet := map[string]bool{}
	for _, m := range allowedModels {
		allowedSet[m] = true
	}
	opts := make([]apiAssistantMode, 0)
	for _, o := range s.config.ModeOptions() {
		if o.ID == controlconf.ExternalClaudeMode {
			if externalAllowed {
				opts = append(opts, apiAssistantMode{ID: o.ID, Label: o.Label})
			}
			continue
		}
		if len(allowedSet) == 0 || allowedSet[o.ID] { // empty allowlist means all models
			opts = append(opts, apiAssistantMode{ID: o.ID, Label: o.Label})
		}
	}
	return opts
}

// resolveMode picks the mode a turn will actually run: the requested mode if the
// user may use it, else the app's remembered mode, else the global default, else
// the first option they have. Empty means the assistant has no usable mode.
func (s *Server) resolveMode(userID, requested, appName string) string {
	opts := s.assistantOptions(userID)
	allowed := func(mode string) bool {
		for _, o := range opts {
			if o.ID == mode {
				return true
			}
		}
		return false
	}
	if allowed(requested) {
		return requested
	}
	if saved, err := s.apps.Store().AppAssistantMode(appName); err == nil && allowed(saved) {
		return saved
	}
	if def := s.defaultMode(); allowed(def) {
		return def
	}
	if len(opts) > 0 {
		return opts[0].ID
	}
	return ""
}

// effectiveUserAssistant returns a user's effective permissions: their explicit
// override if they have one, else the operator's global defaults.
func (s *Server) effectiveUserAssistant(userID string) (externalAllowed bool, allowedModels []string) {
	settings, _ := s.apps.Store().Settings()
	externalAllowed = s.defaultExternalAllowed(settings)
	allowedModels = splitCSV(settings[store.SettingAssistantDefaultModels])
	if userID != "" {
		if override, err := s.apps.Store().UserAssistant(userID); err == nil && override != nil {
			return override.ExternalAllowed, override.AllowedModels
		}
	}
	return externalAllowed, allowedModels
}

// defaultMode is the operator's default selected mode: the configured setting if
// valid, else External Claude when the subscription is set up, else the first API
// model.
func (s *Server) defaultMode() string {
	settings, _ := s.apps.Store().Settings()
	if m := settings[store.SettingAssistantDefaultMode]; m != "" && s.config.IsValidMode(m) {
		return m
	}
	// Prefer an API model as the default; Claude.ai (the subscription) is offered as
	// an additional opt-in whenever its token is present. Only default to Claude.ai
	// when the API is not configured at all. An admin can still set any default above.
	if s.config.AssistantEnabled() {
		return s.config.DefaultAPIModel()
	}
	if s.config.ClaudeBackendEnabled() {
		return controlconf.ExternalClaudeMode
	}
	return s.config.DefaultAPIModel()
}

// defaultExternalAllowed is whether users without an override may use External
// Claude: the configured setting, or (unset) allowed whenever the subscription is
// configured -- the operator set it up, so new users get it by default.
func (s *Server) defaultExternalAllowed(settings map[string]string) bool {
	if v, ok := settings[store.SettingAssistantDefaultExternal]; ok {
		return v == "1"
	}
	return s.config.ClaudeBackendEnabled()
}

// fillUserAssistant sets a user response's effective assistant permissions and
// whether they come from an explicit per-user override.
func (s *Server) fillUserAssistant(r *apiUserResponse, userID string) {
	ext, models := s.effectiveUserAssistant(userID)
	r.AssistantExternalAllowed = ext
	r.AssistantAllowedModels = models
	if r.AssistantAllowedModels == nil {
		r.AssistantAllowedModels = []string{}
	}
	override, err := s.apps.Store().UserAssistant(userID)
	r.AssistantHasOverride = err == nil && override != nil
}

// applyUserAssistant persists a per-user permission change from an admin update:
// clearing the override, or setting one (merging with the current effective
// values for any field the request omits).
func (s *Server) applyUserAssistant(userID string, req *apiUpdateUserRequest) error {
	if req.AssistantClearOverride {
		return s.apps.Store().DeleteUserAssistant(userID)
	}
	if req.AssistantExternalAllowed == nil && req.AssistantAllowedModels == nil {
		return nil // nothing to change
	}
	ext, models := s.effectiveUserAssistant(userID)
	if req.AssistantExternalAllowed != nil {
		ext = *req.AssistantExternalAllowed
	}
	if req.AssistantAllowedModels != nil {
		models = *req.AssistantAllowedModels
	}
	return s.apps.Store().SetUserAssistant(userID, ext, models)
}

// splitCSV parses a comma-separated allowlist; empty means "no restriction".
func splitCSV(csv string) []string {
	out := make([]string, 0)
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
