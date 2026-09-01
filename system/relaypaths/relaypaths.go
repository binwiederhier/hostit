// Package relaypaths holds the fixed on-disk locations the SSH-relay frontend
// uses. The frontend is hostit-control: its `relay-sync` helper writes them (as
// root, from the spec control computes) and its `relay` forwarder reads them at
// ssh time. One definition so the writer and the reader cannot drift. The tree
// is root-owned and lives apart from any component's StateDirectory, since the
// stubs are real Unix accounts the reconcile owns.
package relaypaths

const (
	// Dir is the root-owned directory holding every relay file below.
	Dir = "/var/lib/hostit/relay"
	// Routes is the app<TAB>host routing table the relay forwarder consults.
	Routes = Dir + "/ssh-routes"
	// KnownHosts verifies the frontend->node inner hop.
	KnownHosts = Dir + "/relay_known_hosts"
	// Keys holds each routed app's authorized_keys, one file per app, that the
	// frontend stub account serves.
	Keys = Dir + "/relay-keys"
	// Stubs is where the frontend stub accounts are homed -- apart from the apps
	// pool, so nothing else ever reaps them.
	Stubs = Dir + "/relay-stubs"
	// Key is the frontend's relay PRIVATE key: the credential the forwarder uses
	// to ssh to a node as the app user. Generated on first sync; root-only.
	Key = Dir + "/relay_key"
	// PubKey is the frontend's relay public key, added to remote apps'
	// authorized_keys so the frontend can ssh in as the app.
	PubKey = Dir + "/relay_key.pub"
)
