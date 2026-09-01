package steamcmd

import (
	"errors"
	"regexp"
)

var buildIDPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?im)"buildid"\s*"([0-9]+)"`),
	regexp.MustCompile(`(?im)\bbuildid\b\s*[:=]\s*"?([0-9]+)"?`),
	regexp.MustCompile(`(?im)\bbuildid\b\s+"?([0-9]+)"?`),
}

// ParseBuildID extracts the first numeric buildid field from SteamCMD output
// or a raw app manifest.
func ParseBuildID(output string) (string, error) {
	for _, pattern := range buildIDPatterns {
		match := pattern.FindStringSubmatch(output)
		if len(match) == 2 {
			return match[1], nil
		}
	}
	return "", errors.New("build ID not found")
}
