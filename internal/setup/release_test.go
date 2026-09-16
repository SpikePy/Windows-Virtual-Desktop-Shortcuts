package setup

import "testing"

func TestTagFromLocation(t *testing.T) {
	tests := []struct {
		name, location, want string
		ok                   bool
	}{
		{"absolute URL", "https://github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/releases/tag/v0.1.2", "v0.1.2", true},
		{"relative URL", "/SpikePy/Windows-Virtual-Desktop-Shortcuts/releases/tag/v1.0.0", "v1.0.0", true},
		{"query string", "https://github.com/o/r/releases/tag/v2.3.4?foo=1", "v2.3.4", true},
		{"trailing slash", "https://github.com/o/r/releases/tag/v2.3.4/", "v2.3.4", true},
		{"no releases yet", "https://github.com/o/r/releases", "", false},
		{"empty tag", "https://github.com/o/r/releases/tag/", "", false},
		{"empty header", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tagFromLocation(tt.location)
			if (err == nil) != tt.ok || got != tt.want {
				t.Errorf("tagFromLocation(%q) = %q, %v; want %q, ok %t", tt.location, got, err, tt.want, tt.ok)
			}
		})
	}
}

func TestReleaseURLsNeverUseTheAPI(t *testing.T) {
	for _, u := range []string{latestURL(), downloadURL("X.exe")} {
		if want := "https://github.com/" + repoOwner + "/" + repoName + "/releases/latest"; len(u) < len(want) || u[:len(want)] != want {
			t.Errorf("%q does not start with %q", u, want)
		}
	}
	if got, want := downloadURL("X.exe"), "https://github.com/SpikePy/Windows-Virtual-Desktop-Shortcuts/releases/latest/download/X.exe"; got != want {
		t.Errorf("downloadURL = %q, want %q", got, want)
	}
}
