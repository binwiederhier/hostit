package app

import (
	"os"
	"time"
)

// NopSystemOps is a SystemOps that does nothing; useful for tests and dry runs
type NopSystemOps struct{}

// NopRunner is a Runner that does nothing; useful for tests and dry runs
type NopRunner struct{}

var (
	_ SystemOps = (*NopSystemOps)(nil)
	_ Runner    = (*NopRunner)(nil)
)

// NewNopSystemOps returns a no-op SystemOps
func NewNopSystemOps() SystemOps {
	return &NopSystemOps{}
}

// NewNopRunner returns a no-op Runner
func NewNopRunner() Runner {
	return &NopRunner{}
}

func (o *NopSystemOps) UserExists(username string) bool {
	return false
}

func (o *NopSystemOps) LookupUID(username string) (int, error) {
	return 1001, nil
}

func (o *NopSystemOps) LookupIDs(username string) (IDs, error) {
	return IDs{UID: 1001, GID: 1001, SubUID: 100000, SubGID: 100000, SubCount: 65536}, nil
}

func (o *NopSystemOps) CreateUser(username, home string) error {
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

func (o *NopSystemOps) WriteUserFile(username, home, relPath, content string, mode os.FileMode) error {
	return nil
}

func (o *NopSystemOps) ChownToUser(username, path string) error {
	return nil
}

func (o *NopSystemOps) ApplyPortRules(rules []PortRule) error {
	return nil
}

func (o *NopSystemOps) ImageExists(tag string) bool {
	return true
}

func (o *NopSystemOps) BuildImage(contextDir, tag string) error {
	return nil
}

func (r *NopRunner) Run(args ...string) (string, error) {
	return "", nil
}

func (r *NopRunner) RunTimeout(timeout time.Duration, args ...string) (string, error) {
	return "", nil
}
