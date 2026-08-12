// Package apptest provides no-op test doubles for the app package, kept out of
// production code: NopSystemOps and NopRunner let a test build a Manager without
// touching the host.
package apptest

import (
	"os"

	"heckel.io/hostit/app"
	"heckel.io/hostit/run"
)

// NopSystemOps is a SystemOps that does nothing; useful for tests and dry runs
type NopSystemOps struct{}

var _ app.SystemOps = (*NopSystemOps)(nil)

// NewNopSystemOps returns a no-op SystemOps
func NewNopSystemOps() app.SystemOps {
	return &NopSystemOps{}
}

// NewNopRunner returns a no-op run.Runner, kept as an app-level constructor for
// tests that build a Manager without touching the host.
func NewNopRunner() run.Runner {
	return run.Nop{}
}

func (o *NopSystemOps) UserExists(username string) bool {
	return false
}

func (o *NopSystemOps) LookupUID(username string) (int, error) {
	return 1001, nil
}

func (o *NopSystemOps) LookupIDs(username string) (app.IDs, error) {
	return app.IDs{UID: 1001, GID: 1001, Count: 65536}, nil
}

func (o *NopSystemOps) CreateUser(username, home string, uid int) error {
	return nil
}

func (o *NopSystemOps) RenameUser(oldName, newName string) error {
	return nil
}

func (o *NopSystemOps) KillUserProcesses(username string) error {
	return nil
}

func (o *NopSystemOps) DeleteUser(username string) error {
	return nil
}

func (o *NopSystemOps) WriteAuthorizedKeys(username, home string, keys []string) error {
	return nil
}

func (o *NopSystemOps) WriteScaffold(username, home string, files map[string]string) error {
	return nil
}

func (o *NopSystemOps) ChownToUserIn(root *os.Root, username, rel string) error {
	return nil
}

func (o *NopSystemOps) ApplyPortRules(rules []app.PortRule) error {
	return nil
}
