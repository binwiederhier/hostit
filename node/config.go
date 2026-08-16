package node

import "heckel.io/hostit/nodeconf"

// The node's configuration lives in its own leaf package (nodeconf) so that
// reading it does not drag in this package's machine stack; these aliases keep
// it spelled node.Config everywhere the node itself uses it.
type Config = nodeconf.Config

const (
	// DefaultConfigFile is where a node's config lives on a package install.
	DefaultConfigFile = nodeconf.DefaultConfigFile
	legacyConfigFile  = nodeconf.LegacyConfigFile
)

var (
	NewConfig  = nodeconf.NewConfig
	LoadConfig = nodeconf.LoadConfig
)
