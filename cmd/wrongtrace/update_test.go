package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.3.17", "v0.3.17", 0},
		{"v0.3.9", "v0.3.17", -1},
		{"v0.4.0", "v0.3.17", 1},
		{"0.3.17", "v0.3.17", 0},
		{"v1.0.0-rc1", "v1.0.0", 0},
		{"dev", "v0.3.17", -1},
		{"v0.3.17", "dev", 1},
		{"dev", "dev", 0},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestReleaseAssetName(t *testing.T) {
	cases := map[string]string{
		"linux/amd64":   "wrongtrace-v1.2.3-linux-amd64",
		"darwin/arm64":  "wrongtrace-v1.2.3-darwin-arm64",
		"darwin/amd64":  "wrongtrace-v1.2.3-darwin-amd64",
		"windows/amd64": "wrongtrace-v1.2.3-windows-amd64.exe",
	}
	for plat, want := range cases {
		goos, goarch, _ := strings.Cut(plat, "/")
		got, err := releaseAssetName("v1.2.3", goos, goarch)
		if err != nil || got != want {
			t.Errorf("releaseAssetName(%s) = %q, %v; want %q", plat, got, err, want)
		}
	}
	if _, err := releaseAssetName("v1.2.3", "linux", "arm64"); err == nil {
		t.Error("expected an error for an unpublished platform")
	}
}

func TestParseChecksums(t *testing.T) {
	body := "aaa  wrongtrace-v1-linux-amd64\nbbb *wrongtrace-v1-windows-amd64.exe\n"
	if got, err := parseChecksums(strings.NewReader(body), "wrongtrace-v1-windows-amd64.exe"); err != nil || got != "bbb" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := parseChecksums(strings.NewReader(body), "missing"); err == nil {
		t.Fatal("expected an error for an unlisted asset")
	}
}

func TestLatestReleaseTagFollowsRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.Redirect(w, r, "/releases/tag/v9.8.7", http.StatusFound)
		case "/releases/tag/v9.8.7":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	prev := releaseBaseURL
	releaseBaseURL = srv.URL
	defer func() { releaseBaseURL = prev }()

	tag, err := latestReleaseTag(context.Background(), srv.Client())
	if err != nil || tag != "v9.8.7" {
		t.Fatalf("latestReleaseTag = %q, %v; want v9.8.7", tag, err)
	}
}
