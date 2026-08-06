package archiver_test

import (
	"archive/tar"
	"github.com/jfrog/archiver/v3"
	"os"
	"path"
	"testing"
)

type tarEntry struct {
	name     string
	typeflag byte
	linkname string
	body     []byte
}

func writeTarFile(t *testing.T, dest string, entries []tarEntry) {
	t.Helper()

	f, err := os.Create(dest)
	if err != nil {
		t.Fatalf("creating tar file '%s': %v", dest, err)
	}
	defer f.Close()

	tw := tar.NewWriter(f)
	defer tw.Close()

	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Linkname: e.linkname,
			Mode:     0644,
			Size:     int64(len(e.body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing tar header for '%s': %v", e.name, err)
		}
		if len(e.body) > 0 {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatalf("writing tar body for '%s': %v", e.name, err)
			}
		}
	}
}

func requireRegularFile(t *testing.T, path string) os.FileInfo {
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("fileInfo on '%s': %v", path, err)
	}

	if !fileInfo.Mode().IsRegular() {
		t.Fatalf("'%s' expected to be a regular file", path)
	}

	return fileInfo
}

func assertSameFile(t *testing.T, f1, f2 os.FileInfo) {
	if !os.SameFile(f1, f2) {
		t.Errorf("expected '%s' and '%s' to be the same file", f1.Name(), f2.Name())
	}
}

func TestDefaultTar_Unarchive_HardlinkSuccess(t *testing.T) {
	source := "testdata/gnu-hardlinks.tar"

	destination, err := os.MkdirTemp("", "archiver_tar_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(destination)

	err = archiver.DefaultTar.Unarchive(source, destination)
	if err != nil {
		t.Fatalf("unarchiving '%s' to '%s': %v", source, destination, err)
	}

	fileaInfo := requireRegularFile(t, path.Join(destination, "dir-1", "dir-2", "file-a"))
	filebInfo := requireRegularFile(t, path.Join(destination, "dir-1", "dir-2", "file-b"))
	assertSameFile(t, fileaInfo, filebInfo)
}

func TestDefaultTar_Extract_HardlinkSuccess(t *testing.T) {
	source := "testdata/gnu-hardlinks.tar"

	destination, err := os.MkdirTemp("", "archiver_tar_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(destination)

	err = archiver.DefaultTar.Extract(source, path.Join("dir-1", "dir-2"), destination)
	if err != nil {
		t.Fatalf("unarchiving '%s' to '%s': %v", source, destination, err)
	}

	fileaInfo := requireRegularFile(t, path.Join(destination, "dir-2", "file-a"))
	filebInfo := requireRegularFile(t, path.Join(destination, "dir-2", "file-b"))
	assertSameFile(t, fileaInfo, filebInfo)
}

// TestDefaultTar_Unarchive_HardlinkPathTraversalBlocked is a regression test for JGC-536:
// a hardlink entry whose Linkname escapes the extraction destination must not be
// materialized, since resolving it does not respect the destination boundary
// (e.g. it could otherwise be used to hardlink an arbitrary host file into the
// extracted output for later exfiltration).
func TestDefaultTar_Unarchive_HardlinkPathTraversalBlocked(t *testing.T) {
	tmp, err := os.MkdirTemp("", "archiver_tar_traversal_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	secretPath := path.Join(tmp, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("top-secret"), 0644); err != nil {
		t.Fatalf("writing secret file: %v", err)
	}

	destination := path.Join(tmp, "dest")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatalf("creating destination dir: %v", err)
	}

	evilTar := path.Join(tmp, "evil.tar")
	writeTarFile(t, evilTar, []tarEntry{
		{name: "loot", typeflag: tar.TypeLink, linkname: "../secret.txt"},
	})

	if err := archiver.DefaultTar.Unarchive(evilTar, destination); err != nil {
		t.Fatalf("unarchiving '%s' to '%s': %v", evilTar, destination, err)
	}

	if _, err := os.Lstat(path.Join(destination, "loot")); !os.IsNotExist(err) {
		t.Fatalf("expected 'loot' to not be created, got err=%v", err)
	}
}

// TestDefaultTar_Unarchive_SymlinkPathTraversalBlocked is a regression test for JGC-536:
// a symlink entry whose target escapes the extraction destination must not be
// materialized.
func TestDefaultTar_Unarchive_SymlinkPathTraversalBlocked(t *testing.T) {
	tmp, err := os.MkdirTemp("", "archiver_tar_traversal_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	destination := path.Join(tmp, "dest")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatalf("creating destination dir: %v", err)
	}

	evilTar := path.Join(tmp, "evil.tar")
	writeTarFile(t, evilTar, []tarEntry{
		{name: "loot", typeflag: tar.TypeSymlink, linkname: "../../etc/passwd"},
	})

	if err := archiver.DefaultTar.Unarchive(evilTar, destination); err != nil {
		t.Fatalf("unarchiving '%s' to '%s': %v", evilTar, destination, err)
	}

	if _, err := os.Lstat(path.Join(destination, "loot")); !os.IsNotExist(err) {
		t.Fatalf("expected 'loot' symlink to not be created, got err=%v", err)
	}
}

// TestDefaultTar_Unarchive_SymlinkWithinDestinationSucceeds ensures the path
// traversal guard does not regress legitimate symlinks pointing within the
// extracted tree.
func TestDefaultTar_Unarchive_SymlinkWithinDestinationSucceeds(t *testing.T) {
	tmp, err := os.MkdirTemp("", "archiver_tar_symlink_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	archivePath := path.Join(tmp, "good.tar")
	writeTarFile(t, archivePath, []tarEntry{
		{name: "target.txt", typeflag: tar.TypeReg, body: []byte("hello")},
		{name: "link.txt", typeflag: tar.TypeSymlink, linkname: "target.txt"},
	})

	destination := path.Join(tmp, "dest")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatalf("creating destination dir: %v", err)
	}

	if err := archiver.DefaultTar.Unarchive(archivePath, destination); err != nil {
		t.Fatalf("unarchiving '%s' to '%s': %v", archivePath, destination, err)
	}

	target, err := os.Readlink(path.Join(destination, "link.txt"))
	if err != nil {
		t.Fatalf("reading symlink: %v", err)
	}
	if target != "target.txt" {
		t.Fatalf("expected symlink target 'target.txt', got '%s'", target)
	}
}
