package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// releaseBaseURL is the GitHub repository that publishes release binaries.
// Overridable in tests.
var releaseBaseURL = "https://github.com/WrongStack/WrongTrace"

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update the wrongtrace binary to the latest GitHub release",
	Long: `Download the latest (or a specific) WrongTrace release for this platform,
verify its SHA-256 checksum when the release publishes one, and replace the
running binary in place. Restart a running daemon afterwards to pick it up.`,
	Args: cobra.NoArgs,
	RunE: runUpdate,
}

func init() {
	updateCmd.Flags().Bool("check", false, "only report whether a newer release is available")
	updateCmd.Flags().String("version", "", "install this release tag (e.g. v0.3.17) instead of the latest; allows downgrades")
	updateCmd.Flags().Bool("force", false, "reinstall even when already on the target version")
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, _ []string) error {
	checkOnly, _ := cmd.Flags().GetBool("check")
	wantTag, _ := cmd.Flags().GetString("version")
	force, _ := cmd.Flags().GetBool("force")
	out := cmd.OutOrStdout()

	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
	defer cancel()
	client := &http.Client{}

	tag := strings.TrimSpace(wantTag)
	if tag != "" && !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if tag == "" {
		latest, err := latestReleaseTag(ctx, client)
		if err != nil {
			return fmt.Errorf("resolve latest release: %w", err)
		}
		tag = latest
	}

	fmt.Fprintf(out, "Current version: %s\n", version)
	fmt.Fprintf(out, "Target version:  %s\n", tag)

	cmp := compareVersions(version, tag)
	if checkOnly {
		if cmp < 0 {
			fmt.Fprintln(out, "A newer release is available. Run `wrongtrace update` to install it.")
		} else {
			fmt.Fprintln(out, "Already up to date.")
		}
		return nil
	}
	if !force {
		if cmp == 0 {
			fmt.Fprintln(out, "Already up to date.")
			return nil
		}
		if cmp > 0 && wantTag == "" {
			fmt.Fprintln(out, "Installed version is newer than the latest release; nothing to do (use --version to downgrade).")
			return nil
		}
	}

	asset, err := releaseAssetName(tag, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)

	// A previous Windows update leaves the replaced binary behind because a
	// running .exe cannot be deleted; it is unlocked by now.
	_ = os.Remove(exe + ".old")

	// Stage next to the target so the final rename stays on one volume.
	tmp, err := os.CreateTemp(dir, ".wrongtrace-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s (%w); re-run with sufficient permissions or reinstall with the install script", dir, err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	url := fmt.Sprintf("%s/releases/download/%s/%s", releaseBaseURL, tag, asset)
	fmt.Fprintf(out, "Downloading %s\n", url)
	sum, err := downloadTo(ctx, client, url, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}

	want, err := fetchChecksum(ctx, client, tag, asset)
	switch {
	case err != nil:
		return fmt.Errorf("fetch checksums: %w", err)
	case want == "":
		fmt.Fprintln(out, "Note: this release publishes no checksums.txt; skipping checksum verification.")
	case !strings.EqualFold(want, sum):
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", asset, want, sum)
	default:
		fmt.Fprintln(out, "Checksum verified.")
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod new binary: %w", err)
	}
	if err := smokeTestBinary(ctx, tmpPath); err != nil {
		return fmt.Errorf("downloaded binary failed to run: %w", err)
	}

	if err := replaceExecutable(exe, tmpPath); err != nil {
		return err
	}
	committed = true

	fmt.Fprintf(out, "Updated %s to %s.\n", exe, tag)
	fmt.Fprintln(out, "Restart any running `wrongtrace start` daemon to use the new version.")
	return nil
}

// latestReleaseTag resolves the newest release tag by following the
// /releases/latest redirect, which avoids the GitHub API rate limit.
func latestReleaseTag(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, releaseBaseURL+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, req.URL)
	}
	final := resp.Request.URL.Path
	idx := strings.LastIndex(final, "/tag/")
	if idx < 0 {
		return "", fmt.Errorf("no published release found (resolved to %s)", resp.Request.URL)
	}
	return final[idx+len("/tag/"):], nil
}

// releaseAssetName mirrors the naming in .github/workflows/release.yml.
func releaseAssetName(tag, goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "linux/amd64", "darwin/amd64", "darwin/arm64":
		return fmt.Sprintf("wrongtrace-%s-%s-%s", tag, goos, goarch), nil
	case "windows/amd64":
		return fmt.Sprintf("wrongtrace-%s-%s-%s.exe", tag, goos, goarch), nil
	}
	return "", fmt.Errorf("no prebuilt release binary for %s/%s; build from source instead", goos, goarch)
}

func downloadTo(ctx context.Context, client *http.Client, url string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), resp.Body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fetchChecksum returns the expected SHA-256 for asset from the release's
// checksums.txt, or "" when the release predates published checksums.
func fetchChecksum(ctx context.Context, client *http.Client, tag, asset string) (string, error) {
	url := fmt.Sprintf("%s/releases/download/%s/checksums.txt", releaseBaseURL, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return parseChecksums(resp.Body, asset)
}

func parseChecksums(r io.Reader, asset string) (string, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			return fields[0], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s not listed in checksums.txt", asset)
}

func smokeTestBinary(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// replaceExecutable swaps newPath into exe. Unix allows renaming over a
// running binary; Windows does not allow overwriting it but does allow
// renaming it aside, so the old image moves to exe+".old" first.
func replaceExecutable(exe, newPath string) error {
	if runtime.GOOS != "windows" {
		if err := os.Rename(newPath, exe); err != nil {
			return fmt.Errorf("replace %s: %w", exe, err)
		}
		return nil
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("move current binary aside: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		if rerr := os.Rename(old, exe); rerr != nil {
			return fmt.Errorf("install new binary: %w (restore also failed: %v; previous binary is at %s)", err, rerr, old)
		}
		return fmt.Errorf("install new binary: %w", err)
	}
	return nil
}

// compareVersions orders two vMAJOR.MINOR.PATCH tags. A non-release build
// ("dev" or anything unparseable) sorts before every release.
func compareVersions(a, b string) int {
	pa, okA := parseSemver(a)
	pb, okB := parseSemver(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
