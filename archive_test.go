package steamcmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type tarEntry struct {
	name     string
	typeFlag byte
	data     string
	link     string
}

func makeTarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var data bytes.Buffer
	gzipWriter := gzip.NewWriter(&data)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o644, Typeflag: entry.typeFlag, Linkname: entry.link}
		if entry.typeFlag == tar.TypeDir {
			header.Mode = 0o755
			header.Name = strings.TrimSuffix(entry.name, "/") + "/"
		} else {
			header.Size = int64(len(entry.data))
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.typeFlag == tar.TypeReg {
			if _, err := io.WriteString(tarWriter, entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func makeZip(t *testing.T, entries ...struct {
	name string
	data string
	mode os.FileMode
},
) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		header.SetMode(entry.mode)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestArchiveExtractionAndLinuxChmod(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "steamcmd.tar.gz")
	data := makeTarGz(t,
		tarEntry{name: "steamcmd.sh", typeFlag: tar.TypeReg, data: "#!/bin/sh\n"},
		tarEntry{name: "nested/data.txt", typeFlag: tar.TypeReg, data: "data"},
	)
	if err := os.WriteFile(archivePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "out")
	if err := extractArchive(archivePath, destination, PlatformLinux); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(destination, "steamcmd.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatal("entrypoint is not a regular file")
	}

	root2 := t.TempDir()
	steamDir := filepath.Join(root2, "steamcmd")
	installDir := filepath.Join(root2, "install")
	downloads := 0
	client, err := New(Config{
		SteamCMDDir:  steamDir,
		InstallDir:   installDir,
		Platform:     PlatformLinux,
		Attempts:     1,
		DownloadURLs: []string{"archive"},
		Downloader: func(_ context.Context, _ string, path string) error {
			downloads++
			return os.WriteFile(path, data, 0o644)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(filepath.Join(steamDir, "steamcmd.sh"))
	if err != nil || (runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
		t.Fatalf("installed entrypoint info = %v, %v", info, err)
	}
	if err := client.Install(context.Background()); err != nil || downloads != 1 {
		t.Fatalf("second install err = %v, downloads = %d", err, downloads)
	}
}

func TestArchiveTraversalLinksAndFailedInstallPreserveOld(t *testing.T) {
	for _, test := range []struct {
		name     string
		data     []byte
		platform Platform
	}{
		{"tar traversal", makeTarGz(t, tarEntry{name: "../outside", typeFlag: tar.TypeReg, data: "bad"}), PlatformLinux},
		{"tar symlink", makeTarGz(t, tarEntry{name: "steamcmd.sh", typeFlag: tar.TypeSymlink, link: "/tmp/target"}), PlatformLinux},
		{"tar hardlink", makeTarGz(t, tarEntry{name: "steamcmd.sh", typeFlag: tar.TypeLink, link: "other"}), PlatformLinux},
		{"zip traversal", makeZip(t, struct {
			name string
			data string
			mode os.FileMode
		}{name: "..\\escape", data: "bad", mode: 0o644}), PlatformWindows},
		{"zip symlink", makeZip(t, struct {
			name string
			data string
			mode os.FileMode
		}{name: "steamcmd.exe", data: "/tmp/target", mode: os.ModeSymlink | 0o777}), PlatformWindows},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			archivePath := filepath.Join(root, "archive")
			if err := os.WriteFile(archivePath, test.data, 0o644); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "out")
			if err := extractArchive(archivePath, destination, test.platform); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("partial destination remains: %v", err)
			}
		})
	}

	root := t.TempDir()
	steamDir := filepath.Join(root, "steamcmd")
	installDir := filepath.Join(root, "install")
	if err := os.MkdirAll(steamDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(steamDir, "old-marker")
	if err := os.WriteFile(oldPath, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{
		SteamCMDDir:  steamDir,
		InstallDir:   installDir,
		Platform:     PlatformLinux,
		Attempts:     1,
		DownloadURLs: []string{"bad"},
		Downloader: func(_ context.Context, _ string, path string) error {
			return os.WriteFile(path, []byte("not an archive"), 0o644)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Install(context.Background()); err == nil {
		t.Fatal("invalid installation archive was accepted")
	}
	data, err := os.ReadFile(oldPath)
	if err != nil || string(data) != "old" {
		t.Fatalf("old installation = %q, %v", data, err)
	}
}

func TestArchiveEntryLimits(t *testing.T) {
	entries := make([]struct {
		name string
		data string
		mode os.FileMode
	}, maxArchiveEntries+1)
	for i := range entries {
		entries[i].name = filepath.Join("files", string(rune('a'+i/26)), "file-"+string(rune('a'+i%26)))
		entries[i].data = "x"
		entries[i].mode = 0o644
	}
	root := t.TempDir()
	zipPath := filepath.Join(root, "many.zip")
	if err := os.WriteFile(zipPath, makeZip(t, entries...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(zipPath, filepath.Join(root, "zip-out"), PlatformWindows); err == nil {
		t.Fatal("zip entry limit was not enforced")
	}

	archive := makeOversizedTarGz(maxArchiveFileBytes + 1)
	tarPath := filepath.Join(root, "large.tar.gz")
	if err := os.WriteFile(tarPath, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(tarPath, filepath.Join(root, "tar-out"), PlatformLinux); err == nil {
		t.Fatal("tar file-size limit was not enforced")
	}
}

func TestArchiveHelpersAndFormatErrors(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  string
		isDir bool
		err   bool
	}{
		{name: "empty", err: true},
		{name: "nul", input: "bad\x00name", err: true},
		{name: "unix absolute", input: "/tmp/file", err: true},
		{name: "windows absolute", input: `C:\\file`, err: true},
		{name: "dot", input: ".", want: ".", isDir: true},
		{name: "traversal", input: "a/../../file", err: true},
		{name: "directory", input: "a/b/", want: filepath.Join("a", "b"), isDir: true},
		{name: "file", input: "a/b", want: filepath.Join("a", "b")},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, isDir, err := archiveName(test.input)
			if test.err {
				if err == nil {
					t.Fatal("invalid archive name was accepted")
				}
				return
			}
			if err != nil || got != test.want || isDir != test.isDir {
				t.Fatalf("archiveName() = %q, %t, %v", got, isDir, err)
			}
		})
	}
	if archiveFileMode(0) != 0o644 || archiveFileMode(0o755) != 0o755 {
		t.Fatal("archive file modes were not normalized")
	}

	root := t.TempDir()
	if err := makeArchiveDir(root, "nested"); err != nil {
		t.Fatal(err)
	}
	if err := makeArchiveDir(root, "nested"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makeArchiveDir(root, "file"); err == nil {
		t.Fatal("file was accepted as archive directory")
	}
	oldArchiveLstat := archiveLstat
	oldArchiveMkdir := archiveMkdir
	t.Cleanup(func() {
		archiveLstat = oldArchiveLstat
		archiveMkdir = oldArchiveMkdir
	})
	archiveLstat = func(path string) (os.FileInfo, error) {
		switch filepath.Base(path) {
		case "forced-file":
			return archiveTestFileInfo{mode: 0o644}, nil
		case "forced-error":
			return nil, errors.New("archive lstat failure")
		case "forced-mkdir":
			return nil, os.ErrNotExist
		default:
			return oldArchiveLstat(path)
		}
	}
	if err := makeArchiveDir(root, "forced-file"); err == nil {
		t.Fatal("forced file archive directory was accepted")
	}
	if err := makeArchiveDir(root, "forced-error"); err == nil {
		t.Fatal("archive lstat failure was ignored")
	}
	archiveMkdir = func(string, os.FileMode) error { return errors.New("archive mkdir failure") }
	if err := makeArchiveDir(root, "forced-mkdir"); err == nil {
		t.Fatal("archive mkdir failure was ignored")
	}
	archiveMkdir = oldArchiveMkdir
	if err := verifyArchiveParents(root, "missing/file"); err == nil {
		t.Fatal("missing archive parent was accepted")
	}
	if err := makeArchiveParent(filepath.Join(root, "file"), "child/file"); err == nil {
		t.Fatal("file root was accepted")
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "nested"), filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		if err := verifyArchiveParents(root, "link/file"); err == nil {
			t.Fatal("symlink archive parent was accepted")
		}
	}

	missingDestination := filepath.Join(root, "destination-file")
	if err := os.WriteFile(missingDestination, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "valid.tar.gz")
	if err := os.WriteFile(archivePath, makeTarGz(t, tarEntry{name: "file", typeFlag: tar.TypeReg, data: "x"}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(archivePath, missingDestination, PlatformLinux); err == nil {
		t.Fatal("file destination was accepted")
	}
	if err := extractArchive(filepath.Join(root, "missing.tar.gz"), filepath.Join(root, "out"), PlatformLinux); err == nil {
		t.Fatal("missing archive was accepted")
	}
	if err := extractArchive(archivePath, filepath.Join(root, "out"), PlatformWindows); err == nil {
		t.Fatal("tar data was accepted as ZIP")
	}
	if err := extractArchive(filepath.Join(root, "bad.tar.gz"), filepath.Join(root, "bad-out"), PlatformLinux); err == nil {
		t.Fatal("invalid gzip was accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "bad.tar.gz"), []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}

	zipPath := filepath.Join(root, "valid.zip")
	zipData := makeZip(t,
		struct {
			name string
			data string
			mode os.FileMode
		}{name: "dir/", mode: os.ModeDir | 0o755},
		struct {
			name string
			data string
			mode os.FileMode
		}{name: "dir/file", data: "content", mode: 0o640},
	)
	if err := os.WriteFile(zipPath, zipData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(zipPath, filepath.Join(root, "zip-out"), PlatformWindows); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "zip-out", "dir", "file")); err != nil || string(data) != "content" {
		t.Fatalf("ZIP file = %q, %v", data, err)
	}
	conflictingDirectory := filepath.Join(root, "conflicting-out")
	if err := os.MkdirAll(conflictingDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conflictingDirectory, "dir"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(zipPath, conflictingDirectory, PlatformWindows); err == nil {
		t.Fatal("conflicting ZIP directory was accepted")
	}
	fileParentConflict := filepath.Join(root, "file-parent-conflict")
	if err := os.MkdirAll(fileParentConflict, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fileParentConflict, "dir"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileParentZip := makeZip(t, struct {
		name string
		data string
		mode os.FileMode
	}{name: "dir/file", data: "x", mode: 0o644})
	if err := extractArchive(filepathForArchive(t, root, fileParentZip), fileParentConflict, PlatformWindows); err == nil {
		t.Fatal("conflicting ZIP file parent was accepted")
	}

	oldEntries := maxArchiveEntries
	maxArchiveEntries = 0
	oneEntryZip := makeZip(t, struct {
		name string
		data string
		mode os.FileMode
	}{name: "one", data: "x", mode: 0o644})
	eocd := len(oneEntryZip) - 22
	binary.LittleEndian.PutUint16(oneEntryZip[eocd+8:eocd+10], 0)
	binary.LittleEndian.PutUint16(oneEntryZip[eocd+10:eocd+12], 0)
	if err := extractArchive(filepathForArchive(t, root, oneEntryZip), filepath.Join(root, "entry-count-out"), PlatformWindows); err == nil {
		t.Fatal("ZIP entry count limit was not enforced")
	}
	maxArchiveEntries = oldEntries

	badZipHeader := make([]byte, 22)
	binary.LittleEndian.PutUint32(badZipHeader[0:4], 0x06054b50)
	binary.LittleEndian.PutUint16(badZipHeader[8:10], 1)
	binary.LittleEndian.PutUint16(badZipHeader[10:12], 1)
	binary.LittleEndian.PutUint32(badZipHeader[18:22], 100)
	badZipHeaderPath := filepath.Join(root, "bad-header.zip")
	if err := os.WriteFile(badZipHeaderPath, badZipHeader, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(badZipHeaderPath, filepath.Join(root, "bad-header-out"), PlatformWindows); err == nil {
		t.Fatal("invalid ZIP central directory was accepted")
	}

	file, err := os.Open(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := extractZip(failingArchiveReader{err: errors.New("stat failed")}, filepath.Join(root, "stat-out")); err == nil {
		t.Fatal("stat failure was ignored")
	}
	_ = file.Close()
}

func TestArchiveZipAndTarErrorPaths(t *testing.T) {
	root := t.TempDir()

	badZip := makeZip(t, struct {
		name string
		data string
		mode os.FileMode
	}{name: "file", data: "x", mode: 0o644})
	setZipMethod(badZip, 99)
	badZipPath := filepath.Join(root, "unsupported.zip")
	if err := os.WriteFile(badZipPath, badZip, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(badZipPath, filepath.Join(root, "unsupported-out"), PlatformWindows); err == nil {
		t.Fatal("unsupported ZIP method was accepted")
	}

	duplicateZip := makeZip(t,
		struct {
			name string
			data string
			mode os.FileMode
		}{name: "same", data: "one", mode: 0o644},
		struct {
			name string
			data string
			mode os.FileMode
		}{name: "same", data: "two", mode: 0o644},
	)
	duplicatePath := filepath.Join(root, "duplicate.zip")
	if err := os.WriteFile(duplicatePath, duplicateZip, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(duplicatePath, filepath.Join(root, "duplicate-out"), PlatformWindows); err == nil {
		t.Fatal("duplicate ZIP entry was accepted")
	}

	for _, mode := range []os.FileMode{os.ModeSymlink | 0o777, os.ModeNamedPipe | 0o644} {
		data := makeZip(t, struct {
			name string
			data string
			mode os.FileMode
		}{name: "special", data: "x", mode: mode})
		path := filepath.Join(root, fmt.Sprintf("special-%o.zip", mode))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := extractArchive(path, filepath.Join(root, "special-out"), PlatformWindows); err == nil {
			t.Fatalf("special ZIP mode %v was accepted", mode)
		}
	}

	oldFileOpen := openArchiveEntry
	oldInputClose := closeArchiveInput
	oldOutputClose := closeArchiveOutput
	oldNewReader := archiveNewReader
	t.Cleanup(func() {
		openArchiveEntry = oldFileOpen
		closeArchiveInput = oldInputClose
		closeArchiveOutput = oldOutputClose
		archiveNewReader = oldNewReader
	})
	openArchiveEntry = func(*zip.File) (io.ReadCloser, error) {
		return nil, errors.New("entry open failed")
	}
	validZipPath := filepath.Join(root, "entry-open.zip")
	if err := os.WriteFile(validZipPath, makeZip(t, struct {
		name string
		data string
		mode os.FileMode
	}{name: "file", data: "x", mode: 0o644}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(validZipPath, filepath.Join(root, "entry-open-out"), PlatformWindows); err == nil {
		t.Fatal("entry open failure was ignored")
	}
	archiveNewReader = func(io.ReaderAt, int64) (*zip.Reader, error) {
		return &zip.Reader{File: make([]*zip.File, maxArchiveEntries+1)}, nil
	}
	if err := extractArchive(validZipPath, filepath.Join(root, "entry-count-out"), PlatformWindows); err == nil {
		t.Fatal("ZIP reader entry count limit was not enforced")
	}
	archiveNewReader = oldNewReader
	openArchiveEntry = func(*zip.File) (io.ReadCloser, error) {
		return io.NopCloser(errorReadCloser{}), nil
	}
	if err := extractArchive(validZipPath, filepath.Join(root, "copy-error-out"), PlatformWindows); err == nil {
		t.Fatal("entry copy failure was ignored")
	}
	openArchiveEntry = func(*zip.File) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("123")), nil
	}
	maxArchiveFileBytes = 2
	maxArchiveTotalBytes = 100
	if err := extractArchive(validZipPath, filepath.Join(root, "written-file-limit-out"), PlatformWindows); err == nil {
		t.Fatal("written ZIP file-size limit was not enforced")
	}
	maxArchiveFileBytes = 100
	maxArchiveTotalBytes = 2
	if err := extractArchive(validZipPath, filepath.Join(root, "written-total-limit-out"), PlatformWindows); err == nil {
		t.Fatal("written ZIP total-size limit was not enforced")
	}
	maxArchiveTotalBytes = 1
	if err := extractArchive(filepathForArchive(t, root, makeZip(t, struct {
		name string
		data string
		mode os.FileMode
	}{name: "pre-total", data: "12", mode: 0o644})), filepath.Join(root, "pre-total-out"), PlatformWindows); err == nil {
		t.Fatal("ZIP pre-copy total-size limit was not enforced")
	}
	maxArchiveTotalBytes = 100
	maxArchiveTotalBytes = 100
	openArchiveEntry = func(*zip.File) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("")), nil
	}
	if err := extractArchive(validZipPath, filepath.Join(root, "size-mismatch-out"), PlatformWindows); err == nil {
		t.Fatal("ZIP size mismatch was accepted")
	}

	openArchiveEntry = func(entry *zip.File) (io.ReadCloser, error) {
		return entry.Open()
	}
	closeArchiveInput = func(input io.Closer) error {
		_ = input.Close()
		return errors.New("entry close failed")
	}
	if err := extractArchive(validZipPath, filepath.Join(root, "entry-close-out"), PlatformWindows); err == nil {
		t.Fatal("entry close failure was ignored")
	}
	closeArchiveInput = oldInputClose
	closeArchiveOutput = func(output *os.File) error {
		_ = output.Close()
		return errors.New("output close failed")
	}
	if err := extractArchive(validZipPath, filepath.Join(root, "output-close-out"), PlatformWindows); err == nil {
		t.Fatal("output close failure was ignored")
	}

	oldFileBytes := maxArchiveFileBytes
	oldTotalBytes := maxArchiveTotalBytes
	oldEntries := maxArchiveEntries
	t.Cleanup(func() {
		maxArchiveFileBytes = oldFileBytes
		maxArchiveTotalBytes = oldTotalBytes
		maxArchiveEntries = oldEntries
	})
	maxArchiveFileBytes = 2
	maxArchiveTotalBytes = 3
	if err := extractArchive(filepathForArchive(t, root, makeZip(t, struct {
		name string
		data string
		mode os.FileMode
	}{name: "large", data: "123", mode: 0o644})), filepath.Join(root, "large-out"), PlatformWindows); err == nil {
		t.Fatal("ZIP file-size limit was not enforced")
	}
	if err := extractArchive(filepathForArchive(t, root, makeZip(t,
		struct {
			name string
			data string
			mode os.FileMode
		}{name: "one", data: "12", mode: 0o644},
		struct {
			name string
			data string
			mode os.FileMode
		}{name: "two", data: "34", mode: 0o644},
	)), filepath.Join(root, "total-out"), PlatformWindows); err == nil {
		t.Fatal("ZIP total-size limit was not enforced")
	}
	maxArchiveFileBytes = 100
	maxArchiveTotalBytes = 100
	maxArchiveEntries = 0
	if err := extractArchive(filepathForArchive(t, root, makeTarGz(t, tarEntry{name: "file", typeFlag: tar.TypeReg, data: "x"})), filepath.Join(root, "entry-limit-out"), PlatformLinux); err == nil {
		t.Fatal("TAR entry limit was not enforced")
	}

	for _, entry := range []tarEntry{
		{name: "fifo", typeFlag: tar.TypeFifo},
		{name: "symlink", typeFlag: tar.TypeSymlink, link: "target"},
		{name: "hardlink", typeFlag: tar.TypeLink, link: "target"},
	} {
		maxArchiveEntries = 100
		path := filepathForArchive(t, root, makeTarGz(t, entry))
		if err := extractArchive(path, filepath.Join(root, "tar-type-out"), PlatformLinux); err == nil {
			t.Fatalf("TAR type %d was accepted", entry.typeFlag)
		}
	}

	shortTar := filepathForArchive(t, root, makeShortTarGz())
	if err := extractArchive(shortTar, filepath.Join(root, "short-tar-out"), PlatformLinux); err == nil {
		t.Fatal("short TAR entry was accepted")
	}
	closeArchiveOutput = func(output *os.File) error {
		_ = output.Close()
		return errors.New("tar output close failed")
	}
	if err := extractArchive(filepathForArchive(t, root, makeTarGz(t, tarEntry{name: "file", typeFlag: tar.TypeReg, data: "x"})), filepath.Join(root, "tar-close-out"), PlatformLinux); err == nil {
		t.Fatal("TAR output close failure was ignored")
	}

	closeArchiveOutput = oldOutputClose
	maxArchiveFileBytes = 100
	maxArchiveTotalBytes = 1
	if err := extractArchive(filepathForArchive(t, root, makeTarGz(t, tarEntry{name: "large", typeFlag: tar.TypeReg, data: "xx"})), filepath.Join(root, "tar-total-out"), PlatformLinux); err == nil {
		t.Fatal("TAR total-size limit was not enforced")
	}
	maxArchiveTotalBytes = 100
	tarDirectoryConflict := filepath.Join(root, "tar-directory-conflict")
	if err := os.MkdirAll(tarDirectoryConflict, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tarDirectoryConflict, "dir"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(filepathForArchive(t, root, makeTarGz(t, tarEntry{name: "dir", typeFlag: tar.TypeDir})), tarDirectoryConflict, PlatformLinux); err == nil {
		t.Fatal("conflicting TAR directory was accepted")
	}
	tarParentConflict := filepath.Join(root, "tar-parent-conflict")
	if err := os.MkdirAll(tarParentConflict, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tarParentConflict, "parent"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(filepathForArchive(t, root, makeTarGz(t, tarEntry{name: "parent/file", typeFlag: tar.TypeReg, data: "x"})), tarParentConflict, PlatformLinux); err == nil {
		t.Fatal("conflicting TAR parent was accepted")
	}
	tarDuplicate := filepathForArchive(t, root, makeTarGz(t,
		tarEntry{name: "same", typeFlag: tar.TypeReg, data: "one"},
		tarEntry{name: "same", typeFlag: tar.TypeReg, data: "two"},
	))
	if err := extractArchive(tarDuplicate, filepath.Join(root, "tar-duplicate-out"), PlatformLinux); err == nil {
		t.Fatal("duplicate TAR entry was accepted")
	}
	if err := extractArchive(filepathForArchive(t, root, makeInvalidTarGz()), filepath.Join(root, "invalid-tar-out"), PlatformLinux); err == nil {
		t.Fatal("invalid TAR header was accepted")
	}

	if err := checkZipEOCD(bytes.NewReader(make([]byte, 21)), 21); err == nil {
		t.Fatal("short ZIP was accepted")
	}
	if err := checkZipEOCD(failingReadAt{}, 22); err == nil {
		t.Fatal("ZIP read failure was ignored")
	}
	commentZip := make([]byte, 22)
	binary.LittleEndian.PutUint32(commentZip[0:4], 0x06054b50)
	binary.LittleEndian.PutUint16(commentZip[20:22], 1)
	if err := checkZipEOCD(bytes.NewReader(commentZip), int64(len(commentZip))); err == nil {
		t.Fatal("truncated ZIP comment was accepted")
	}
}

type failingArchiveReader struct {
	err error
}

type archiveTestFileInfo struct {
	mode os.FileMode
}

func (f archiveTestFileInfo) Name() string       { return "test" }
func (f archiveTestFileInfo) Size() int64        { return 0 }
func (f archiveTestFileInfo) Mode() os.FileMode  { return f.mode }
func (f archiveTestFileInfo) ModTime() time.Time { return time.Time{} }
func (f archiveTestFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f archiveTestFileInfo) Sys() any           { return nil }

type failingReadAt struct{}

func (failingReadAt) ReadAt([]byte, int64) (int, error) {
	return 0, errors.New("read failed")
}

func (failingArchiveReader) ReadAt([]byte, int64) (int, error) {
	return 0, errors.New("read failed")
}

func (failingArchiveReader) Stat() (os.FileInfo, error) {
	return nil, errors.New("stat failed")
}

func setZipMethod(data []byte, method uint16) {
	for index := 0; index+10 <= len(data); index++ {
		if bytes.Equal(data[index:index+4], []byte{'P', 'K', 3, 4}) {
			data[index+8] = byte(method)
			data[index+9] = byte(method >> 8)
		}
		if bytes.Equal(data[index:index+4], []byte{'P', 'K', 1, 2}) {
			data[index+10] = byte(method)
			data[index+11] = byte(method >> 8)
		}
	}
}

func filepathForArchive(t *testing.T, root string, data []byte) string {
	t.Helper()
	path := filepath.Join(root, fmt.Sprintf("archive-%d", len(data)))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func makeShortTarGz() []byte {
	var data bytes.Buffer
	gzipWriter := gzip.NewWriter(&data)
	tarWriter := tar.NewWriter(gzipWriter)
	_ = tarWriter.WriteHeader(&tar.Header{Name: "short", Mode: 0o644, Typeflag: tar.TypeReg, Size: 2})
	_, _ = io.WriteString(tarWriter, "x")
	_ = tarWriter.Flush()
	_ = gzipWriter.Close()
	return data.Bytes()
}

func makeInvalidTarGz() []byte {
	var data bytes.Buffer
	gzipWriter := gzip.NewWriter(&data)
	_, _ = gzipWriter.Write([]byte{'x'})
	_ = gzipWriter.Close()
	return data.Bytes()
}

func makeOversizedTarGz(size int64) []byte {
	header := make([]byte, 512)
	copy(header[0:100], "too-large")
	copy(header[100:108], "0000644\x00")
	copy(header[108:116], "0000000\x00")
	copy(header[116:124], "0000000\x00")
	copy(header[124:136], fmt.Sprintf("%011o", size))
	header[156] = tar.TypeReg
	copy(header[257:263], "ustar\x00")
	copy(header[263:265], "00")
	for i := 148; i < 156; i++ {
		header[i] = ' '
	}
	var checksum int
	for _, value := range header {
		checksum += int(value)
	}
	copy(header[148:156], fmt.Sprintf("%06o\x00 ", checksum))
	header = append(header, make([]byte, 1024)...)
	data := header
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	_, _ = gzipWriter.Write(data)
	_ = gzipWriter.Close()
	return compressed.Bytes()
}
