package app

import (
	"os"
)

// NopSystemOps is a SystemOps that does nothing; useful for tests and dry runs
type NopSystemOps struct{}

// NopUserRunner is a UserRunner that does nothing; useful for tests and dry runs
type NopUserRunner struct{}

var (
	_ SystemOps  = (*NopSystemOps)(nil)
	_ UserRunner = (*NopUserRunner)(nil)
)

// NewNopSystemOps returns a no-op SystemOps
func NewNopSystemOps() SystemOps {
	return &NopSystemOps{}
}

// NewNopUserRunner returns a no-op UserRunner
func NewNopUserRunner() UserRunner {
	return &NopUserRunner{}
}

func (o *NopSystemOps) UserExists(username string) bool {
	return false
}

func (o *NopSystemOps) LookupUID(username string) (int, error) {
	return 1001, nil
}

func (o *NopSystemOps) CreateUser(username, home string) error {
	return nil
}

func (o *NopSystemOps) DeleteUser(username string) error {
	return nil
}

func (o *NopSystemOps) EnableLinger(username string) error {
	return nil
}

func (o *NopSystemOps) WriteAuthorizedKeys(username, home string, keys []string) error {
	return nil
}

func (o *NopSystemOps) WriteScaffold(username, home string, files map[string]string) error {
	return nil
}

func (o *NopSystemOps) WriteUserFile(username, home, relPath, content string, mode os.FileMode) error {
	return nil
}

func (o *NopSystemOps) ApplyPortRules(rules []PortRule) error {
	return nil
}

func (r *NopUserRunner) RunAsUser(username string, args ...string) (string, error) {
	return "", nil
}
