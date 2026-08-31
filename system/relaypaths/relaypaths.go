// Package relaypaths holds the fixed on-disk locations the SSH-relay frontend
// uses. Shared by the node -- which writes them from the spec control pushes
// over the cluster link -- and the hostit-relay helper, which reads them at ssh
// time. One definition so the writer and the reader cannot drift.
package relaypaths

const (
	// Routes is the app<TAB>host routing table the relay helper consults.
	Routes = "/var/lib/hostit/node/ssh-routes"
	// KnownHosts verifies the frontend->node inner hop.
	KnownHosts = "/etc/hostit/relay_known_hosts"
	// Keys holds each routed app's authorized_keys, one file per app, that the
	// frontend stub account serves.
	Keys = "/var/lib/hostit/node/relay-keys"
	// Stubs is where the frontend stub accounts are homed -- outside the apps
	// pool, so the app reconcile never reaps them.
	Stubs = "/var/lib/hostit/node/relay-stubs"
	// PubKey is the frontend's relay public key; the node reports it so control
	// adds it to remote apps' authorized_keys.
	PubKey = "/etc/hostit/relay_key.pub"
)
