package connections

// The providers this proof of concept knows. Google proves the OAuth path;
// GitHub and IMAP prove the abstraction is not OAuth-shaped -- IMAP has no
// OAuth at all and still fits, which is the point.

func init() {
	// Scopes are read-only on purpose: a dashboard correlating calendar and
	// mail never needs to write, and a connection that cannot write is a much
	// smaller thing to hand an app.
	Register(Provider{
		Name:  "google",
		Label: "Google",
		Kind:  KindOAuth,
		Scopes: []string{
			"https://www.googleapis.com/auth/calendar.readonly",
			"https://www.googleapis.com/auth/gmail.readonly",
			"https://www.googleapis.com/auth/userinfo.email",
		},
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		Help:     "Calendar and Gmail, read-only.",
	})

	Register(Provider{
		Name:        "github",
		Label:       "GitHub",
		Kind:        KindStatic,
		SecretField: "token",
		Fields: []Field{
			{Name: "token", Label: "Personal access token", Placeholder: "github_pat_...", Secret: true},
		},
		Help: "A fine-grained personal access token from github.com/settings/tokens.",
	})

	Register(Provider{
		Name:        "imap",
		Label:       "IMAP mailbox",
		Kind:        KindStatic,
		SecretField: "password",
		Fields: []Field{
			{Name: "host", Label: "Server", Placeholder: "imap.example.com:993"},
			{Name: "username", Label: "Username", Placeholder: "you@example.com"},
			{Name: "password", Label: "Password", Placeholder: "an app password", Secret: true},
		},
		Help: "Use an app password, not your account password.",
	})
}
