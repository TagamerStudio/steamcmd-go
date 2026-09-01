package steamcmd

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var (
	maxArchiveFileBytes  int64 = 2 << 30
	maxArchiveTotalBytes int64 = 10 << 30
	maxArchiveEntries          = 10000
)

var (
	openArchiveEntry = func(entry *zip.File) (io.ReadCloser, error) {
		return entry.Open()
	}
	closeArchiveInput = func(input io.Closer) error {
		return input.Close()
	}
	closeArchiveOutput = func(output *os.File) error {
		return output.Close()
	}
	archiveLstat     = os.Lstat
	archiveMkdir     = os.Mkdir
	archiveNewReader = zip.NewReader
)

func extractArchive(archivePath, destination string, platform Platform) (err error) {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(destination)
		}
	}()
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if platform == PlatformWindows {
		return extractZip(file, destination)
	}
	return extractTarGz(file, destination)
}

type archiveReader interface {
	io.ReaderAt
	Stat() (os.FileInfo, error)
}

func extractZip(file archiveReader, destination string) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := checkZipEOCD(file, info.Size()); err != nil {
		return err
	}
	reader, err := archiveNewReader(file, info.Size())
	if err != nil {
		return err
	}
	if len(reader.File) > maxArchiveEntries {
		return fmt.Errorf("archive contains more than %d entries", maxArchiveEntries)
	}
	return extractZipEntries(reader.File, destination)
}

func extractZipEntries(entries []*zip.File, destination string) error {
	var total int64
	for _, entry := range entries {
		written, err := extractZipEntry(destination, entry, total)
		if err != nil {
			return err
		}
		total += written
	}
	return nil
}

func extractZipEntry(destination string, entry *zip.File, total int64) (int64, error) {
	name, isDir, err := validateZipEntry(entry, total)
	if err != nil {
		return 0, err
	}
	if isDir {
		if err := makeArchiveDir(destination, name); err != nil {
			return 0, err
		}
		return 0, nil
	}
	written, err := writeZipEntry(destination, name, entry)
	if err != nil {
		return 0, err
	}
	if written > maxArchiveFileBytes {
		return 0, fmt.Errorf("archive entry %q exceeds maximum file size", entry.Name)
	}
	if written > maxArchiveTotalBytes-total {
		return 0, errors.New("archive exceeds maximum extracted size")
	}
	if written != int64(entry.UncompressedSize64) {
		return 0, errors.New("archive entry size mismatch")
	}
	return written, nil
}

func validateZipEntry(entry *zip.File, total int64) (string, bool, error) {
	name, isDir, err := archiveName(entry.Name)
	if err != nil {
		return "", false, err
	}
	if entry.Mode().IsDir() {
		isDir = true
	}
	if entry.Mode()&os.ModeSymlink != 0 {
		return "", false, fmt.Errorf("archive contains symlink entry %q", entry.Name)
	}
	if entry.Mode()&os.ModeType != 0 && !isDir {
		return "", false, fmt.Errorf("archive contains special entry %q", entry.Name)
	}
	if isDir {
		return name, true, nil
	}
	if entry.UncompressedSize64 > uint64(maxArchiveFileBytes) {
		return "", false, fmt.Errorf("archive entry %q exceeds maximum file size", entry.Name)
	}
	if int64(entry.UncompressedSize64) > maxArchiveTotalBytes-total {
		return "", false, errors.New("archive exceeds maximum extracted size")
	}
	return name, false, nil
}

func writeZipEntry(destination, name string, entry *zip.File) (int64, error) {
	if err := makeArchiveParent(destination, name); err != nil {
		return 0, err
	}
	output, err := os.OpenFile(safeJoin(destination, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, archiveFileMode(entry.Mode()))
	if err != nil {
		return 0, err
	}
	input, err := openArchiveEntry(entry)
	if err != nil {
		_ = output.Close()
		return 0, err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, maxArchiveFileBytes+1))
	inputCloseErr := closeArchiveInput(input)
	closeErr := closeArchiveOutput(output)
	if copyErr != nil {
		return 0, copyErr
	}
	if inputCloseErr != nil {
		return 0, inputCloseErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	return written, nil
}

func extractTarGz(file *os.File, destination string) error {
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	var total int64
	entries := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("archive contains more than %d entries", maxArchiveEntries)
		}
		written, err := extractTarEntry(reader, destination, header, total)
		if err != nil {
			return err
		}
		total += written
	}
	return nil
}

func extractTarEntry(reader *tar.Reader, destination string, header *tar.Header, total int64) (int64, error) {
	name, _, err := archiveName(header.Name)
	if err != nil {
		return 0, err
	}
	switch header.Typeflag {
	case tar.TypeDir:
		if err := makeArchiveDir(destination, name); err != nil {
			return 0, err
		}
		return 0, nil
	case tar.TypeReg:
		return extractTarRegular(reader, destination, name, header, total)
	case tar.TypeSymlink, tar.TypeLink:
		return 0, fmt.Errorf("archive contains link entry %q", header.Name)
	default:
		return 0, fmt.Errorf("archive contains unsupported entry %q", header.Name)
	}
}

func extractTarRegular(reader *tar.Reader, destination, name string, header *tar.Header, total int64) (int64, error) {
	if header.Size < 0 || header.Size > maxArchiveFileBytes {
		return 0, fmt.Errorf("archive entry %q exceeds maximum file size", header.Name)
	}
	if header.Size > maxArchiveTotalBytes-total {
		return 0, errors.New("archive exceeds maximum extracted size")
	}
	if err := makeArchiveParent(destination, name); err != nil {
		return 0, err
	}
	output, err := os.OpenFile(safeJoin(destination, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, archiveFileMode(header.FileInfo().Mode()))
	if err != nil {
		return 0, err
	}
	written, copyErr := io.CopyN(output, reader, header.Size)
	closeErr := closeArchiveOutput(output)
	if copyErr != nil {
		return 0, copyErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	return written, nil
}

func checkZipEOCD(reader io.ReaderAt, size int64) error {
	if size < 22 {
		return errors.New("zip archive is too small")
	}
	windowSize := int64(22 + 1<<16)
	if windowSize > size {
		windowSize = size
	}
	window := make([]byte, windowSize)
	if _, err := reader.ReadAt(window, size-windowSize); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	for i := len(window) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(window[i:i+4]) != 0x06054b50 {
			continue
		}
		commentLength := int(binary.LittleEndian.Uint16(window[i+20 : i+22]))
		if i+22+commentLength > len(window) {
			continue
		}
		entries := binary.LittleEndian.Uint16(window[i+10 : i+12])
		if entries == 0xffff || int(entries) > maxArchiveEntries {
			return fmt.Errorf("zip archive contains more than %d entries", maxArchiveEntries)
		}
		return nil
	}
	return errors.New("zip end of central directory not found")
}

func archiveName(name string) (string, bool, error) {
	if name == "" {
		return "", false, errors.New("archive contains an empty entry name")
	}
	if strings.ContainsRune(name, '\x00') {
		return "", false, errors.New("archive entry contains NUL")
	}
	name = strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(name, "/") || path.IsAbs(name) || isWindowsAbsolute(name) {
		return "", false, fmt.Errorf("archive entry %q is absolute", name)
	}
	clean := path.Clean(name)
	if clean == "." {
		return ".", true, nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false, fmt.Errorf("archive entry %q escapes destination", name)
	}
	isDir := strings.HasSuffix(name, "/")
	return filepathFromSlash(clean), isDir, nil
}

func isWindowsAbsolute(name string) bool {
	return len(name) >= 3 && ((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z')) && name[1] == ':' && name[2] == '/'
}

func filepathFromSlash(value string) string {
	return strings.ReplaceAll(value, "/", string(os.PathSeparator))
}

func safeJoin(root, name string) string {
	return filepathFromSlash(root + "/" + name)
}

func makeArchiveParent(root, name string) error {
	parent := filepathFromSlash(root + "/" + filepathToSlash(filepathDir(name)))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	return verifyArchiveParents(root, name)
}

func makeArchiveDir(root, name string) error {
	if err := makeArchiveParent(root, name+"/entry"); err != nil {
		return err
	}
	dir := safeJoin(root, name)
	if info, err := archiveLstat(dir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("archive path %q is not a directory", name)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return archiveMkdir(dir, 0o755)
}

func verifyArchiveParents(root, name string) error {
	parts := strings.Split(filepathToSlash(filepathDir(name)), "/")
	current := root
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, filepathFromSlash(part))
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("archive path component %q is unsafe", part)
		}
	}
	return nil
}

func filepathToSlash(value string) string {
	return strings.ReplaceAll(value, string(os.PathSeparator), "/")
}

func filepathDir(value string) string {
	index := strings.LastIndex(filepathToSlash(value), "/")
	if index < 0 {
		return "."
	}
	return filepathToSlash(value)[:index]
}

func archiveFileMode(mode os.FileMode) os.FileMode {
	mode &= 0o777
	if mode == 0 {
		return 0o644
	}
	return mode
}
