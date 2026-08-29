// Package platformdoc holds prose about the hostit platform that is TRUE
// regardless of who is acting on an app -- the external coding agent reading
// /info, or the built-in web assistant working through its tools. Both import
// these snippets so the shared facts (the preview, hostit.yml, the layout)
// never drift between the two prompts.
package platformdoc

// PreviewNote explains the live preview and, in particular, the ?hostit_preview
// query parameter and the private-app caveat -- the two things an agent gets
// wrong when it has to guess. Written to read the same to an external agent and
// to the built-in assistant.
func PreviewNote() string {
	return `The owner sees a LIVE PREVIEW of the app -- the app's real URL loaded in an iframe, beside the workspace and on the dashboard. Three things about it:
- hostit refreshes it by re-requesting the app with a "?hostit_preview=<number>" query parameter. That parameter is hostit's, not yours: serve the request normally and NEVER 404 or error on it. If the app caches, treat a request that carries a query string as uncacheable, so the refresh shows the latest deploy instead of a stale page.
- Do not block framing. "X-Frame-Options: DENY" (or SAMEORIGIN), or a Content-Security-Policy whose frame-ancestors does not include the hostit dashboard, makes the preview blank. Setting neither is the safe default.
- A PRIVATE app may show NOTHING in the preview even when it works perfectly: the iframe loads the app's public URL without the owner's hostit login, so a private app answers it with 403. That is expected and is NOT a bug in the app -- do not try to "fix" it in the code. Opening the app in its own tab (where the owner is signed in), or making it public, shows it.`
}
