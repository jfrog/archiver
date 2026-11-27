package archiver_test

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jfrog/archiver/v3"
)

// newLinksTestDirs creates a temp dir with a "dest" subdir and a file "outside.txt" next to it.
func newLinksTestDirs(t *testing.T) (tmp, destination, outsideFile string) {
	t.Helper()
	tmp = t.TempDir()
	destination = filepath.Join(tmp, "dest")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatalf("creating destination dir: %v", err)
	}
	outsideFile = filepath.Join(tmp, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0644); err != nil {
		t.Fatalf("writing outside file: %v", err)
	}
	return
}

func requireSymlink(t *testing.T, linkPath, expectedTarget string) {
	t.Helper()
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("reading symlink '%s': %v", linkPath, err)
	}
	if target != expectedTarget {
		t.Fatalf("expected symlink '%s' to point to '%s', got '%s'", linkPath, expectedTarget, target)
	}
}

func requireNotExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatalf("expected '%s' to not be created, got err=%v", p, err)
	}
}

func TestTar_Unarchive_AllowLinksOutsideDestination(t *testing.T) {
	tmp, destination, outsideFile := newLinksTestDirs(t)
	archivePath := filepath.Join(tmp, "links.tar")
	writeTarFile(t, archivePath, []tarEntry{
		{name: "abs_link", typeflag: tar.TypeSymlink, linkname: outsideFile},
		{name: "rel_link", typeflag: tar.TypeSymlink, linkname: "../outside.txt"},
		{name: "hard_link", typeflag: tar.TypeLink, linkname: "../outside.txt"},
	})

	tarArchiver := archiver.NewTar()
	tarArchiver.AllowLinksOutsideDestination = true
	if err := tarArchiver.Unarchive(archivePath, destination); err != nil {
		t.Fatalf("unarchiving '%s' to '%s': %v", archivePath, destination, err)
	}

	requireSymlink(t, filepath.Join(destination, "abs_link"), outsideFile)
	requireSymlink(t, filepath.Join(destination, "rel_link"), "../outside.txt")
	assertSameFile(t, requireRegularFile(t, filepath.Join(destination, "hard_link")), requireRegularFile(t, outsideFile))
}

// The default must keep blocking links outside the destination.
func TestTar_Unarchive_LinksOutsideDestinationBlockedByDefault(t *testing.T) {
	tmp, destination, outsideFile := newLinksTestDirs(t)
	archivePath := filepath.Join(tmp, "links.tar")
	writeTarFile(t, archivePath, []tarEntry{
		{name: "abs_link", typeflag: tar.TypeSymlink, linkname: outsideFile},
		{name: "rel_link", typeflag: tar.TypeSymlink, linkname: "../outside.txt"},
		{name: "hard_link", typeflag: tar.TypeLink, linkname: "../outside.txt"},
	})

	if err := archiver.NewTar().Unarchive(archivePath, destination); err != nil {
		t.Fatalf("unarchiving '%s' to '%s': %v", archivePath, destination, err)
	}

	for _, name := range []string{"abs_link", "rel_link", "hard_link"} {
		requireNotExist(t, filepath.Join(destination, name))
	}
}

// AllowLinksOutsideDestination relaxes only the link target check: entries whose own path escapes the destination
// are still rejected.
func TestTar_Unarchive_AllowLinksOutsideDestinationKeepsEntryPathCheck(t *testing.T) {
	tmp, destination, _ := newLinksTestDirs(t)
	archivePath := filepath.Join(tmp, "evil.tar")
	writeTarFile(t, archivePath, []tarEntry{
		{name: "../escaped.txt", typeflag: tar.TypeReg, body: []byte("evil")},
		{name: "../escaped_link", typeflag: tar.TypeSymlink, linkname: "anything"},
	})

	tarArchiver := archiver.NewTar()
	tarArchiver.AllowLinksOutsideDestination = true
	if err := tarArchiver.Unarchive(archivePath, destination); err != nil {
		t.Fatalf("unarchiving '%s' to '%s': %v", archivePath, destination, err)
	}

	requireNotExist(t, filepath.Join(tmp, "escaped.txt"))
	requireNotExist(t, filepath.Join(tmp, "escaped_link"))
}

// The compressed tar formats embed *Tar, so they get the field too.
func TestTarGz_Unarchive_AllowLinksOutsideDestination(t *testing.T) {
	tmp, destination, outsideFile := newLinksTestDirs(t)
	tarPath := filepath.Join(tmp, "links.tar")
	writeTarFile(t, tarPath, []tarEntry{
		{name: "abs_link", typeflag: tar.TypeSymlink, linkname: outsideFile},
	})
	archivePath := tarPath + ".gz"
	gzipFile(t, tarPath, archivePath)

	tarGz := archiver.NewTarGz()
	tarGz.AllowLinksOutsideDestination = true
	if err := tarGz.Unarchive(archivePath, destination); err != nil {
		t.Fatalf("unarchiving '%s' to '%s': %v", archivePath, destination, err)
	}

	requireSymlink(t, filepath.Join(destination, "abs_link"), outsideFile)
}

func TestZip_Unarchive_AllowLinksOutsideDestination(t *testing.T) {
	for _, allow := range []bool{false, true} {
		tmp, destination, outsideFile := newLinksTestDirs(t)
		archivePath := filepath.Join(tmp, "links.zip")
		writeZipWithSymlinks(t, archivePath, map[string]string{
			"abs_link": outsideFile,
			"rel_link": "../outside.txt",
		})

		zipArchiver := archiver.NewZip()
		zipArchiver.AllowLinksOutsideDestination = allow
		if err := zipArchiver.Unarchive(archivePath, destination); err != nil {
			t.Fatalf("allow=%v: unarchiving '%s' to '%s': %v", allow, archivePath, destination, err)
		}

		if allow {
			requireSymlink(t, filepath.Join(destination, "abs_link"), outsideFile)
			requireSymlink(t, filepath.Join(destination, "rel_link"), "../outside.txt")
		} else {
			requireNotExist(t, filepath.Join(destination, "abs_link"))
			requireNotExist(t, filepath.Join(destination, "rel_link"))
		}
	}
}

func writeZipWithSymlinks(t *testing.T, dest string, links map[string]string) {
	t.Helper()
	f, err := os.Create(dest)
	if err != nil {
		t.Fatalf("creating zip file '%s': %v", dest, err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	for name, target := range links {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(os.ModeSymlink | 0777)
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatalf("writing zip header for '%s': %v", name, err)
		}
		if _, err = w.Write([]byte(target)); err != nil {
			t.Fatalf("writing zip body for '%s': %v", name, err)
		}
	}
}

func gzipFile(t *testing.T, source, dest string) {
	t.Helper()
	in, err := os.Open(source)
	if err != nil {
		t.Fatalf("opening '%s': %v", source, err)
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		t.Fatalf("creating '%s': %v", dest, err)
	}
	defer out.Close()
	gw := gzip.NewWriter(out)
	if _, err = io.Copy(gw, in); err != nil {
		t.Fatalf("compressing '%s': %v", source, err)
	}
	if err = gw.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
}
