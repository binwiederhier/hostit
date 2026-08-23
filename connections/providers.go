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

	// -- HubSpot. CRM objects, read-only.
	Register(Provider{
		Name:     "hubspot",
		Label:    "HubSpot",
		Kind:     KindOAuth,
		Scopes:   []string{"oauth", "crm.objects.contacts.read", "crm.objects.companies.read", "crm.objects.deals.read"},
		AuthURL:  "https://app.hubspot.com/oauth/authorize",
		TokenURL: "https://api.hubapi.com/oauth/v1/token",
		Help:     "Read contacts, companies and deals from one HubSpot portal.",
	})

	// -- Linear. Its tokens are effectively permanent and it issues no refresh
	// token, so the access token is what gets stored.
	Register(Provider{
		Name:           "linear",
		Label:          "Linear",
		Kind:           KindOAuth,
		Scopes:         []string{"read"},
		AuthURL:        "https://linear.app/oauth/authorize",
		TokenURL:       "https://api.linear.app/oauth/token",
		LongLivedToken: true,
		Help:           "Read issues, projects and teams from your Linear workspace.",
	})

	// -- Pasted credentials. No OAuth client, no review, no expiry, and no
	// console to visit: the reason the abstraction is "credential" and not
	// "OAuth". Most of what anyone actually wants to connect lives down here.

	// Mail. The Gmail note is deliberate: an app password over IMAP sidesteps
	// verification, CASA, the 100-test-user cap AND the 7-day refresh-token
	// expiry that makes Google's OAuth path painful for a personal instance.
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
		Help: "Any IMAP mailbox: Fastmail, Migadu, or Gmail itself at imap.gmail.com:993 with a Google app password -- which needs no OAuth review at all.",
	})
	Register(Provider{
		Name:        "smtp",
		Label:       "SMTP (sending mail)",
		Kind:        KindStatic,
		SecretField: "password",
		Fields: []Field{
			{Name: "host", Label: "Server", Placeholder: "smtp.fastmail.com:465"},
			{Name: "username", Label: "Username", Placeholder: "you@example.com"},
			{Name: "password", Label: "Password", Placeholder: "an app password", Secret: true},
			{Name: "from", Label: "From address", Placeholder: "optional", Optional: true},
		},
		Help: "Send mail from an app. Gmail works at smtp.gmail.com:465 with an app password.",
	})

	// Calendar and contacts without Google. The URL is not a credential, so it
	// stays out of the secret and the app reads it beside the password.
	Register(Provider{
		Name:        "caldav",
		Label:       "CalDAV calendar",
		Kind:        KindStatic,
		SecretField: "password",
		Fields: []Field{
			{Name: "url", Label: "Server URL", Placeholder: "https://caldav.fastmail.com/dav/calendars"},
			{Name: "username", Label: "Username", Placeholder: "you@fastmail.com"},
			{Name: "password", Label: "Password", Placeholder: "an app password", Secret: true},
		},
		Help: "Fastmail, iCloud, Nextcloud or any CalDAV server. Connect two to cross-reference calendars, with none of Google's review.",
	})
	Register(Provider{
		Name:        "carddav",
		Label:       "CardDAV contacts",
		Kind:        KindStatic,
		SecretField: "password",
		Fields: []Field{
			{Name: "url", Label: "Server URL", Placeholder: "https://carddav.fastmail.com/dav/addressbooks"},
			{Name: "username", Label: "Username", Placeholder: "you@fastmail.com"},
			{Name: "password", Label: "Password", Placeholder: "an app password", Secret: true},
		},
		Help: "Contacts from Fastmail, iCloud, Nextcloud or any CardDAV server.",
	})

	// Databases. One pasted URL, because that is exactly what every managed
	// provider hands you -- splitting it into host/port/sslmode fields only
	// invites the app to reassemble it wrongly.
	Register(Provider{
		Name:        "postgres",
		Label:       "PostgreSQL",
		Kind:        KindStatic,
		SecretField: "url",
		Fields: []Field{
			{Name: "url", Label: "Connection URL", Placeholder: "postgres://user:pass@host:5432/db?sslmode=require", Secret: true},
			{Name: "note", Label: "Note", Placeholder: "which database this is (optional)", Optional: true},
		},
		Help: "An external Postgres. Paste the connection URL your provider gave you.",
	})
	Register(Provider{
		Name:        "mysql",
		Label:       "MySQL / MariaDB",
		Kind:        KindStatic,
		SecretField: "url",
		Fields: []Field{
			{Name: "url", Label: "Connection URL", Placeholder: "mysql://user:pass@host:3306/db", Secret: true},
			{Name: "note", Label: "Note", Placeholder: "which database this is (optional)", Optional: true},
		},
		Help: "An external MySQL or MariaDB. Paste the connection URL.",
	})
	Register(Provider{
		Name:        "opensearch",
		Label:       "OpenSearch / Elasticsearch",
		Kind:        KindStatic,
		SecretField: "password",
		Fields: []Field{
			{Name: "url", Label: "Endpoint", Placeholder: "https://search.example.com:9200"},
			{Name: "username", Label: "Username", Placeholder: "optional for API-key auth", Optional: true},
			{Name: "password", Label: "Password or API key", Placeholder: "", Secret: true},
		},
		Help: "An OpenSearch or Elasticsearch cluster, by basic auth or an API key.",
	})

	// Object storage, which almost every app eventually wants.
	Register(Provider{
		Name:        "s3",
		Label:       "S3 storage",
		Kind:        KindStatic,
		SecretField: "secret-access-key",
		Fields: []Field{
			{Name: "endpoint", Label: "Endpoint", Placeholder: "https://nyc3.digitaloceanspaces.com (blank for AWS)", Optional: true},
			{Name: "region", Label: "Region", Placeholder: "us-east-1", Optional: true},
			{Name: "bucket", Label: "Bucket", Placeholder: "optional", Optional: true},
			{Name: "access-key-id", Label: "Access key ID", Placeholder: "AKIA..."},
			{Name: "secret-access-key", Label: "Secret access key", Placeholder: "", Secret: true},
		},
		Help: "S3 or anything speaking it: DO Spaces, Cloudflare R2, Backblaze B2, MinIO.",
	})

	// Notifications and the homelab.
	Register(Provider{
		Name:        "ntfy",
		Label:       "ntfy",
		Kind:        KindStatic,
		SecretField: "token",
		Fields: []Field{
			{Name: "server", Label: "Server", Placeholder: "https://ntfy.sh (optional)", Optional: true},
			{Name: "topic", Label: "Default topic", Placeholder: "optional", Optional: true},
			{Name: "token", Label: "Access token", Placeholder: "tk_...", Secret: true},
		},
		Help: "Push notifications from an app. Works with ntfy.sh or your own server.",
	})
	Register(Provider{
		Name:        "home-assistant",
		Label:       "Home Assistant",
		Kind:        KindStatic,
		SecretField: "token",
		Fields: []Field{
			{Name: "url", Label: "Base URL", Placeholder: "http://homeassistant.local:8123"},
			{Name: "token", Label: "Long-lived access token", Placeholder: "", Secret: true},
		},
		Help: "Read sensors and call services on your Home Assistant, with a long-lived access token.",
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
