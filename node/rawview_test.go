package node

import "testing"

// The raw apps view must never land inside the directory that gets mounted into
// a container. With the app socket served from /run/hostit/node/app/, its parent
// (/run/hostit/node/app) is what the container sees -- so apps-raw has to sit one
// level up, a sibling of that subdir, at the run root.
func TestRawAppsViewDirIsSiblingOfTheSocketSubdir(t *testing.T) {
	t.Parallel()
	got := RawAppsViewDir("/run/hostit/node/app/hostit.sock")
	want := "/run/hostit/node/apps-raw"
	if got != want {
		t.Fatalf("RawAppsViewDir = %q, want %q (must be outside the mounted %q subdir)", got, want, "/run/hostit/node/app")
	}
}
