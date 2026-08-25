package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// buildTree makes a small workspace: a file, a nested file, and a symlink.
func buildTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi there"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("hello.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	return root
}

func tarEntries(t *testing.T, b []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		if h.Typeflag == tar.TypeSymlink {
			out[h.Name] = "->" + h.Linkname
			continue
		}
		data, _ := io.ReadAll(tr)
		out[h.Name] = string(data)
	}
	return out
}

func TestWriteTarGz(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := Write(buildTree(t), TarGz, &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := tarEntries(t, buf.Bytes())
	if got["hello.txt"] != "hi there" {
		t.Errorf("hello.txt = %q", got["hello.txt"])
	}
	if got["src/main.go"] != "package main" {
		t.Errorf("src/main.go = %q", got["src/main.go"])
	}
	if got["link"] != "->hello.txt" {
		t.Errorf("symlink stored as %q, want a symlink to hello.txt", got["link"])
	}
}

func TestWriteZip(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := Write(buildTree(t), Zip, &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	found := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		found[f.Name] = string(data)
	}
	if found["hello.txt"] != "hi there" {
		t.Errorf("hello.txt = %q", found["hello.txt"])
	}
	if found["src/main.go"] != "package main" {
		t.Errorf("src/main.go = %q", found["src/main.go"])
	}
}
