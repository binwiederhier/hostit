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
		RevokeURL:  "https://oauth2.googleapis.com/revoke",
		AuthParams: google,
		NameHint:   "Work calendar",
		Help:       "Read-only access to one Google account's calendars. Connect it twice to cross-reference two of them.",
	})
	Register(Provider{
		Name:       "gmail",
		Label:      "Gmail",
		Kind:       KindOAuth,
		Scopes:     []string{"https://www.googleapis.com/auth/gmail.readonly", "https://www.googleapis.com/auth/userinfo.email"},
		AuthURL:    "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:   "https://oauth2.googleapis.com/token",
		RevokeURL:  "https://oauth2.googleapis.com/revoke",
		AuthParams: google,
		NameHint:   "Work mail",
		Help:       "Read-only access to one Gmail mailbox. A restricted scope: an instance offering this publicly needs Google's CASA review.",
	})

	// -- Slack (bot). The bot token (xoxb-) does not expire and there is no refresh
	// token, so hostit stores the access token itself. The id is "slack-bot",
	// symmetric with "slack-user"; a store migration rewrites connections made
	// under the old bare "slack" id, and operators rename the config key to match.
	Register(Provider{
		Name:             "slack-bot",
		Label:            "Slack (bot)",
		Kind:             KindOAuth,
		Scopes:           []string{"channels:read", "channels:history", "chat:write", "users:read"},
		AuthURL:          "https://slack.com/oauth/v2/authorize",
		TokenURL:         "https://slack.com/api/oauth.v2.access",
		RevokeURL:        "https://slack.com/api/auth.revoke",
		RevokeAuth:       "bearer",
		LongLivedToken:   true,
		DisallowMultiple: true, // one Slack app = one bot token per workspace
		NameHint:         "Team Slack",
		Help:             "A shared bot in one Slack workspace: it reads and posts in the channels it is invited to. To read the channels you are already in as yourself, use Slack (personal).",
		ShortDescription: "A shared bot that reads and posts in the channels it's invited to.",
		LongDescription: "Slack Web API, base https://slack.com/api. Authenticate with the bot token (xoxb) as a Bearer header. " +
			"The bot only sees channels it has been invited to. Common methods: conversations.list, conversations.history, " +
			"chat.postMessage, users.info. Responses are JSON with {\"ok\": bool}; on ok=false read \"error\". Rate-limited per " +
			"method (HTTP 429 with Retry-After).",
	})

	// -- Slack (personal). A USER token (xoxp-, via user_scope), so it acts as
	// the person who connected it: it reads the channels they are already in and
	// searches across them, with nothing to invite to a channel. The read grant
	// is the owner's to narrow at connect time via ScopeOptions; users:read is the
	// baseline so ids resolve to names. Like the bot token it does not expire.
	Register(Provider{
		Name:           "slack-user",
		Label:          "Slack (personal)",
		Kind:           KindOAuth,
		UserToken:      true,
		LongLivedToken: true,
		Scopes:         []string{"users:read"},
		ScopeOptions: []ScopeOption{
			{Key: "public", Label: "Public channels", Help: "read the public channels you are in", Scopes: []string{"channels:read", "channels:history"}, Default: true},
			{Key: "private", Label: "Private channels", Help: "read private channels you are a member of", Scopes: []string{"groups:read", "groups:history"}},
			{Key: "dms", Label: "Direct messages", Help: "read your direct messages", Scopes: []string{"im:read", "im:history"}},
			{Key: "group-dms", Label: "Group direct messages", Help: "read your group DMs", Scopes: []string{"mpim:read", "mpim:history"}},
			{Key: "search", Label: "Search", Help: "search your messages across every channel you can see", Scopes: []string{"search:read"}},
			{Key: "post", Label: "Post messages", Help: "post and reply in threads as you", Scopes: []string{"chat:write"}},
			{Key: "react", Label: "Add reactions", Help: "add and remove emoji reactions", Scopes: []string{"reactions:write"}},
			{Key: "read-reactions", Label: "Read reactions", Help: "see who reacted to a message", Scopes: []string{"reactions:read"}},
			{Key: "files", Label: "Files", Help: "read and upload files", Scopes: []string{"files:read", "files:write"}},
		},
		AuthURL:          "https://slack.com/oauth/v2/authorize",
		TokenURL:         "https://slack.com/api/oauth.v2.access",
		RevokeURL:        "https://slack.com/api/auth.revoke",
		RevokeAuth:       "bearer",
		DisallowMultiple: true, // one Slack app = one grant per user; a 2nd connection only aliases it
		NameHint:         "My Slack",
		Help:             "Reads the Slack channels you are already in and searches across them, as you -- no bot to invite. To post as a shared bot, use Slack (bot) instead.",
		ShortDescription: "Read the Slack channels you're in and search them, as yourself.",
		LongDescription: "Slack Web API, base https://slack.com/api. Authenticate with the token as a Bearer header " +
			"(Authorization: Bearer <token>) or as the `token` form field; it is a user token (xoxp) that acts as the " +
			"connecting person. Common methods: conversations.list, conversations.history, conversations.replies, " +
			"search.messages (needs the search scope), users.info. Every response is JSON with {\"ok\": bool}; on ok=false " +
			"read the \"error\" field. Methods are rate-limited (HTTP 429 with Retry-After).",
	})

	// -- Discord. Rotates its refresh token on every use, which is exactly why
	// Provider.Refresh reports the new one and the caller stores it.
	Register(Provider{
		Name:      "discord",
		Label:     "Discord",
		Kind:      KindOAuth,
		Scopes:    []string{"identify"}, // baseline: who you are, so a name resolves
		AuthURL:   "https://discord.com/oauth2/authorize",
		TokenURL:  "https://discord.com/api/oauth2/token",
		RevokeURL: "https://discord.com/api/oauth2/token/revoke", // client-authenticated (RFC 7009)
		ScopeOptions: []ScopeOption{
			{Key: "guilds", Label: "Your servers", Help: "which Discord servers you are a member of", Scopes: []string{"guilds"}, Default: true},
			{Key: "email", Label: "Email address", Help: "the email on your Discord account", Scopes: []string{"email"}},
			{Key: "connections", Label: "Linked accounts", Help: "the accounts you have linked to Discord", Scopes: []string{"connections"}},
		},
		NameHint: "My Discord",
		Help:     "Your Discord profile and which servers you are in. Reading a server's channels or messages needs a bot token, not this -- see Discord bot.",
	})

	// A Discord BOT token, pasted. A user's OAuth token can list the servers
	// they are in and nothing inside them; channels and messages are only
	// reachable by a bot that has been invited to the server. Two different
	// credentials for two different jobs, so they are two providers rather than
	// one that half-works.
	Register(Provider{
		Name:        "discord-bot",
		Label:       "Discord bot",
		Kind:        KindStatic,
		SecretField: "token",
		Fields: []Field{
			{Name: "token", Label: "Bot token", Placeholder: "from the Bot tab of your application", Secret: true},
			{Name: "guild", Label: "Server ID", Placeholder: "optional, if the app only uses one", Optional: true},
		},
		NameHint: "Server bot",
		Help:     "Read channels and messages. Create a bot under your Discord application's Bot tab, then invite it to the server. Send the header as \"Authorization: Bot <token>\".",
	})

	// -- GitHub. Hybrid: a classic OAuth App's token never expires and is stored
	// as-is, but a GitHub App (or an OAuth App with expiring user tokens) hands
	// back an 8h access token AND a refresh token, which hostit must actually
	// refresh -- treating that kind as long-lived was why a github connection
	// died overnight and needed reconnecting every morning. Which one a given
	// connection got is decided at Exchange. A fine-grained personal access token
	// via the generic credential is the other route.
	Register(Provider{
		Name:        "github",
		Label:       "GitHub",
		Kind:        KindOAuth,
		Scopes:      []string{"repo", "read:user"},
		AuthURL:     "https://github.com/login/oauth/authorize",
		TokenURL:    "https://github.com/login/oauth/access_token",
		HybridToken: true,
		ProbeURL:    "https://api.github.com/user",
		NameHint:    "GitHub",
		Help:        "Repositories and your profile, as you.",
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
		NameHint: "Team Jira",
		Help:     "Read issues and users in your Atlassian site.",
	})

	// -- HubSpot. CRM objects, read-only.
	Register(Provider{
		Name:     "hubspot",
		Label:    "HubSpot",
		Kind:     KindOAuth,
		Scopes:   []string{"oauth", "crm.objects.contacts.read", "crm.objects.companies.read", "crm.objects.deals.read"},
		AuthURL:  "https://app.hubspot.com/oauth/authorize",
		TokenURL: "https://api.hubapi.com/oauth/v1/token",
		NameHint: "HubSpot",
		Help:     "Read contacts, companies and deals from one HubSpot portal.",
	})

	// -- Linear. Hybrid, like GitHub: a classic Linear token is effectively
	// permanent with no refresh token, but a workspace with token expiration
	// enabled issues an expiring token AND a refresh token, which hostit must
	// refresh rather than serve forever. Which one a connection got is decided at
	// Exchange; either way the GraphQL probe catches a token revoked at Linear
	// (an app reinstall or a permission change) and flags it for reconnect.
	Register(Provider{
		Name:        "linear",
		Label:       "Linear",
		Kind:        KindOAuth,
		Scopes:      []string{"read"},
		AuthURL:     "https://linear.app/oauth/authorize",
		TokenURL:    "https://api.linear.app/oauth/token",
		HybridToken: true,
		ProbeURL:    "https://api.linear.app/graphql",
		ProbeBody:   `{"query":"{ viewer { id } }"}`,
		NameHint:    "Linear",
		Help:        "Read issues, projects and teams from your Linear workspace.",
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
		NameHint: "Work mail",
		Help:     "Any IMAP mailbox: Fastmail, Migadu, or Gmail itself at imap.gmail.com:993 with a Google app password -- which needs no OAuth review at all.",
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
		NameHint: "Outgoing mail",
		Help:     "Send mail from an app. Gmail works at smtp.gmail.com:465 with an app password.",
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
		NameHint: "Work calendar",
		Help:     "Fastmail, iCloud, Nextcloud or any CalDAV server. Connect two to cross-reference calendars, with none of Google's review.",
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
		NameHint: "Contacts",
		Help:     "Contacts from Fastmail, iCloud, Nextcloud or any CardDAV server.",
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
		NameHint: "App database",
		Help:     "An external Postgres. Paste the connection URL your provider gave you.",
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
		NameHint: "App database",
		Help:     "An external MySQL or MariaDB. Paste the connection URL.",
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
		NameHint: "Search cluster",
		Help:     "An OpenSearch or Elasticsearch cluster, by basic auth or an API key.",
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
		NameHint: "Backups bucket",
		Help:     "S3 or anything speaking it: DO Spaces, Cloudflare R2, Backblaze B2, MinIO.",
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
		NameHint: "Alerts",
		Help:     "Push notifications from an app. Works with ntfy.sh or your own server.",
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
		NameHint: "Home Assistant",
		Help:     "Read sensors and call services on your Home Assistant, with a long-lived access token.",
	})

	// Fastmail's JMAP: one scoped API token reaching mail, calendars AND
	// contacts, which is why it is its own provider rather than three separate
	// CalDAV/IMAP/SMTP credentials for the same account.
	Register(Provider{
		Name:        "fastmail",
		Label:       "Fastmail",
		Kind:        KindStatic,
		SecretField: "token",
		NameHint:    "Fastmail",
		Fields: []Field{
			{Name: "token", Label: "API token", Placeholder: "Settings -> Privacy & Security -> API tokens", Secret: true},
			// JMAP clients begin by fetching the session document, which tells
			// them every other URL; leaving this blank means the standard one.
			{Name: "session", Label: "JMAP session URL", Placeholder: "https://api.fastmail.com/jmap/session (leave blank for this)", Optional: true},
		},
		Help: "Mail, calendars and contacts, all from one token. Create it in Fastmail under Settings -> Privacy & Security -> API tokens, ticking only what the app needs.",
	})

	// A private key an app uses to reach another machine or a git remote.
	Register(Provider{
		Name:        "ssh-key",
		Label:       "SSH key",
		Kind:        KindStatic,
		SecretField: "private-key",
		Fields: []Field{
			{
				Name: "private-key", Label: "Private key", Secret: true, Multiline: true,
				Placeholder: "-----BEGIN OPENSSH PRIVATE KEY-----\n...\n-----END OPENSSH PRIVATE KEY-----",
				Pattern:     `-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`,
				PatternHint: "that does not look like a private key -- it should start with -----BEGIN ... PRIVATE KEY-----. An ssh-ed25519 or ssh-rsa line is the PUBLIC half, which is not a credential.",
			},
			{Name: "user", Label: "Username", Placeholder: "git, deploy, ... (optional)", Optional: true},
			{Name: "host", Label: "Host", Placeholder: "example.com:22 (optional)", Optional: true},
		},
		NameHint: "Deploy key",
		Help:     "A private key an app uses to reach another machine or a git remote. Use one with no passphrase -- an app cannot type one.",
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
		NameHint: "OpenAI key",
		Help:     "Any service hostit does not know: paste its API key and the app reads it back.",
	})
}
