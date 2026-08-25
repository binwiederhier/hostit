// Package archive streams a directory tree as a zip or a gzipped tar. It is used
// to export an app's whole workspace: the node snapshots the subvolume, archives
// the snapshot's files through here, then drops the snapshot.
package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Format is the archive format an export produces.
type Format string

const (
	// Zip is the default: a .zip, which opens everywhere.
	Zip = Format("zip")
	// TarGz is a gzipped tar: leaner, and it keeps unix modes and symlinks.
	TarGz = Format("tar")
)

// Ext is the file extension for the format (no leading dot).
func (f Format) Ext() string {
	if f == Zip {
		return "zip"
	}
	return "tar.gz"
}

// Write streams root as an archive in the given format to w. It walks with
// Lstat semantics -- a symlink is stored AS a symlink, never followed -- so a
// file the tenant planted cannot redirect the read out of the tree. Special
// files (sockets, devices, fifos) are skipped.
func Write(root string, format Format, w io.Writer) error {
	if format == Zip {
		return writeZip(root, w)
	}
	return writeTarGz(root, w)
}

func writeTarGz(root string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := walk(root, func(rel string, info fs.FileInfo, link string) error {
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = rel
		if info.IsDir() {
			hdr.Name = rel + "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			return copyInto(tw, filepath.Join(root, rel))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func writeZip(root string, w io.Writer) error {
	zw := zip.NewWriter(w)
	err := walk(root, func(rel string, info fs.FileInfo, link string) error {
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = rel
		if info.IsDir() {
			hdr.Name = rel + "/"
		}
		hdr.Method = zip.Deflate
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			// A zip symlink is the target path written as the entry's content,
			// with the symlink mode bit (already on hdr from FileInfoHeader).
			_, err = io.WriteString(fw, link)
			return err
		}
		if info.Mode().IsRegular() {
			return copyInto(fw, filepath.Join(root, rel))
		}
		return nil
	})
	if err != nil {
		return err
	}
	return zw.Close()
}

func copyInto(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// walk visits every entry under root (not root itself) with its slash-separated
// RELATIVE path, Lstat info (WalkDir does not follow symlinks), and, for a
// symlink, its target. Special files are skipped.
func walk(root string, fn func(rel string, info fs.FileInfo, link string) error) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		link := ""
		if mode&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		} else if !mode.IsRegular() && !mode.IsDir() {
			return nil // sockets, devices, fifos: nothing an archive can carry
		}
		return fn(filepath.ToSlash(rel), info, link)
	})
}
