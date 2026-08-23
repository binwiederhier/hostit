package connections

// The providers hostit knows how to connect.
//
// Two things vary between them and nothing else does: what the consent URL
// needs, and whether the provider hands back something that expires. Google
// and Atlassian issue refresh tokens; Slack and GitHub issue a token that
// simply does not expire. Both end up behind the same token endpoint, so an
// app never learns which it got.
//
// Scopes are read-only wherever the provider offers the choice. A connection
// that cannot write is a much smaller thing to hand an app, and nothing built
// on hostit so far has needed more.

func init() {
	// -- Google. Calendar and mail are SEPARATE providers, each with its own
	// consent, so granting an app the calendar never implies the mailbox.
	// Both need access_type=offline or Google returns no refresh token at all.
	google := map[string]string{
		"access_type": "offline",
		"prompt":      "consent",
		// So adding a second Google connection later does not silently drop
		// scopes already granted to the first.
		"include_granted_scopes": "true",
	}
	Register(Provider{
		Name:       "google-calendar",
		Label:      "Google Calendar",
		Kind:       KindOAuth,
		Scopes:     []string{"https://www.googleapis.com/auth/calendar.readonly", "https://www.googleapis.com/auth/userinfo.email"},
		AuthURL:    "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:   "https://oauth2.googleapis.com/token",
		AuthParams: google,
		Help:       "Read-only access to one Google account's calendars. Connect it twice to cross-reference two of them.",
	})
	Register(Provider{
		Name:       "gmail",
		Label:      "Gmail",
		Kind:       KindOAuth,
		Scopes:     []string{"https://www.googleapis.com/auth/gmail.readonly", "https://www.googleapis.com/auth/userinfo.email"},
		AuthURL:    "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:   "https://oauth2.googleapis.com/token",
		AuthParams: google,
		Help:       "Read-only access to one Gmail mailbox. A restricted scope: an instance offering this publicly needs Google's CASA review.",
	})

	// -- Slack. The bot token (xoxb-) does not expire and there is no refresh
	// token, so hostit stores the access token itself.
	Register(Provider{
		Name:           "slack",
		Label:          "Slack",
		Kind:           KindOAuth,
		Scopes:         []string{"channels:read", "channels:history", "chat:write", "users:read"},
		AuthURL:        "https://slack.com/oauth/v2/authorize",
		TokenURL:       "https://slack.com/api/oauth.v2.access",
		LongLivedToken: true,
		Help:           "A bot in one Slack workspace: read channels and post messages.",
	})

	// -- Discord. Issues a refresh token like Google does.
	Register(Provider{
		Name:     "discord",
		Label:    "Discord",
		Kind:     KindOAuth,
		Scopes:   []string{"identify", "guilds"},
		AuthURL:  "https://discord.com/oauth2/authorize",
		TokenURL: "https://discord.com/api/oauth2/token",
		Help:     "Your Discord identity and the servers you are in.",
	})

	// -- GitHub. An OAuth App's token does not expire (only a GitHub App with
	// expiring user tokens refreshes), so it is stored as-is. A fine-grained
	// personal access token via the generic credential is the other route.
	Register(Provider{
		Name:           "github",
		Label:          "GitHub",
		Kind:           KindOAuth,
		Scopes:         []string{"repo", "read:user"},
		AuthURL:        "https://github.com/login/oauth/authorize",
		TokenURL:       "https://github.com/login/oauth/access_token",
		LongLivedToken: true,
		Help:           "Repositories and your profile, as you.",
	})

	// -- Jira / Atlassian 3LO. Needs its audience named, and offline_access is
	// how it grants a refresh token at all.
	Register(Provider{
		Name:     "jira",
		Label:    "Jira",
		Kind:     KindOAuth,
		Scopes:   []string{"read:jira-work", "read:jira-user", "offline_access"},
		AuthURL:  "https://auth.atlassian.com/authorize",
		TokenURL: "https://auth.atlassian.com/oauth/token",
		AuthParams: map[string]string{
			"audience": "api.atlassian.com",
			"prompt":   "consent",
		},
		Help: "Read issues and users in your Atlassian site.",
	})

	// -- Pasted credentials. No OAuth client, no review, no expiry: the reason
	// the abstraction is "credential" and not "OAuth". Fastmail lands here.
	Register(Provider{
		Name:        "imap",
		Label:       "IMAP mailbox",
		Kind:        KindStatic,
		SecretField: "password",
		Fields: []Field{
			{Name: "host", Label: "Server", Placeholder: "imap.fastmail.com:993"},
			{Name: "username", Label: "Username", Placeholder: "you@example.com"},
			{Name: "password", Label: "Password", Placeholder: "an app password", Secret: true},
		},
		Help: "Any IMAP mailbox, Fastmail included. Use an app password, not your account password.",
	})

	// The escape hatch: anything with an API key. The app gets the secret and
	// whatever context was pasted alongside it, and decides what to do with it.
	Register(Provider{
		Name:        "generic",
		Label:       "API key or token",
		Kind:        KindStatic,
		SecretField: "secret",
		Fields: []Field{
			{Name: "secret", Label: "Secret", Placeholder: "sk-... or any token", Secret: true},
			{Name: "endpoint", Label: "Endpoint", Placeholder: "https://api.example.com (optional)", Optional: true},
			{Name: "note", Label: "Note", Placeholder: "what this is for (optional)", Optional: true},
		},
		Help: "Any service hostit does not know: paste its API key and the app reads it back.",
	})
}
