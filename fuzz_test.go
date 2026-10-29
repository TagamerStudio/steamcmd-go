package steamcmd

import (
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func FuzzParseBuildID(f *testing.F) {
	for _, seed := range []string{
		`"buildid" "123456"`,
		"BuildID : 987",
		`buildid="42"`,
		"no build here",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, output string) {
		buildID, err := ParseBuildID(output)
		if err != nil {
			return
		}
		if buildID == "" {
			t.Fatal("accepted an empty build ID")
		}
		if strings.Trim(buildID, "0123456789") != "" {
			t.Fatalf("accepted a non-numeric build ID: %q", buildID)
		}
	})
}

func FuzzArchiveName(f *testing.F) {
	for _, seed := range []string{
		"world/Level.sav",
		"world/Players/0001.sav",
		"world/",
		".",
		"../escape",
		"/absolute",
		`C:\absolute`,
		"a\\b",
		"a\x00b",
		"..",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		name, _, err := archiveName(raw)
		if err != nil {
			return
		}
		if name == "" {
			t.Fatal("accepted an empty archive name")
		}
		if strings.ContainsRune(name, '\x00') {
			t.Fatalf("accepted NUL in %q", name)
		}
		if name == ".." || strings.HasPrefix(name, "../") {
			t.Fatalf("accepted traversal in %q", name)
		}
		if path.IsAbs(name) || filepath.IsAbs(name) || isWindowsAbsolute(name) {
			t.Fatalf("accepted absolute path %q", name)
		}
	})
}
