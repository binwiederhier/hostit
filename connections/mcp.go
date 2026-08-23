package connections

// The MCP pseudo-provider. Unlike everything else in the catalog, it describes
// no particular service: the owner pastes a URL and hostit asks THAT server what
// it is and what it can do. There is one entry here rather than one per MCP
// server for the same reason there is no entry per website -- the set is open,
// and the protocol is the point.
//
// It is a third kind rather than a static credential because the credential is
// not what an app gets. hostit keeps the token and makes the calls itself; see
// the mcp package and control/mcp.go for why.

// ProviderMCP is the catalog name for the MCP pseudo-provider.
const ProviderMCP = "mcp"

func init() {
	Register(Provider{
		Name:  ProviderMCP,
		Label: "MCP server",
		Kind:  KindMCP,
		Fields: []Field{
			{
				Name:        "url",
				Label:       "Server URL",
				Placeholder: "https://mcp.example.com/mcp",
				Pattern:     `^https?://[^\s]+$`,
				PatternHint: "that does not look like a URL; it should start with https://",
			},
		},
		Help:     "The MCP endpoint URL. hostit asks the server what it needs and walks you through signing in if it wants authorization.",
		NameHint: "issues",
	})
}
