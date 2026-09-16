package setup

import (
	"fmt"
	"strings"
)

// Releases are found through github.com's own "latest" redirects rather
// than the GitHub API, whose unauthenticated rate limit (60 requests an
// hour per IP) makes installs fail on shared or busy networks. This file
// has no OS dependency, so the parsing is tested on any platform.

const (
	repoOwner = "SpikePy"
	repoName  = "Windows-Virtual-Desktop-Shortcuts"
)

// latestURL redirects to the newest release's page, .../releases/tag/<tag>.
func latestURL() string {
	return fmt.Sprintf("https://github.com/%s/%s/releases/latest", repoOwner, repoName)
}

// downloadURL redirects to asset in the newest release.
func downloadURL(asset string) string {
	return latestURL() + "/download/" + asset
}

// tagFromLocation reads the release tag out of the Location header that
// latestURL redirects with.
func tagFromLocation(location string) (string, error) {
	const marker = "/releases/tag/"
	i := strings.Index(location, marker)
	if i < 0 {
		return "", fmt.Errorf("unexpected redirect to %q", location)
	}
	tag := location[i+len(marker):]
	if j := strings.IndexAny(tag, "/?#"); j >= 0 {
		tag = tag[:j]
	}
	if tag == "" {
		return "", fmt.Errorf("no release tag in redirect to %q", location)
	}
	return tag, nil
}
