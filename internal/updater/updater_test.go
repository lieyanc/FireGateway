package updater

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lieyanc/FireGateway/internal/version"
)

// TestCheckOnlyAcrossSourcesAndChannels covers release selection over both
// download sources (proxy mirror, GitHub direct) and both channels
// (stable, dev), including the dev-channel same-commit skip.
func TestCheckOnlyAcrossSourcesAndChannels(t *testing.T) {
	cases := []struct {
		name         string
		cfg          Config
		localVersion string
		localCommit  string
		remote       releaseVersionInfo
		direct       bool // serve the GitHub-direct layout
		wantUpdate   bool
		wantLatest   string
	}{
		{
			name:         "proxy stable selects newest stable release",
			cfg:          Config{Channel: "stable", Source: "proxy", Repo: "owner/repo"},
			localVersion: "v1.0.0",
			remote:       releaseVersionInfo{Tag: "v1.4.0"},
			wantUpdate:   true,
			wantLatest:   "v1.4.0",
		},
		{
			name:         "proxy stable skips older release",
			cfg:          Config{Channel: "stable", Source: "proxy", Repo: "owner/repo"},
			localVersion: "v1.5.0",
			remote:       releaseVersionInfo{Tag: "v1.4.0"},
			wantUpdate:   false,
			wantLatest:   "v1.4.0",
		},
		{
			name:         "proxy dev selects newest prerelease",
			cfg:          Config{Channel: "dev", Source: "proxy", Repo: "owner/repo"},
			localVersion: "dev-0007-20260401-aaaaaaa",
			localCommit:  "aaaaaaa",
			remote:       releaseVersionInfo{Version: "dev-0042-20260425-bbbbbbb", Commit: "bbbbbbb", BuildTime: "2026-04-25T00:00:00Z", Tag: "dev"},
			wantUpdate:   true,
			wantLatest:   "dev-0042-20260425-bbbbbbb",
		},
		{
			name:         "proxy dev skips release for same commit",
			cfg:          Config{Channel: "dev", Source: "proxy", Repo: "owner/repo"},
			localVersion: "dev-0042-20260425-bbbbbbb",
			localCommit:  "bbbbbbb",
			remote:       releaseVersionInfo{Version: "dev-0042-20260425-bbbbbbb", Commit: "bbbbbbb", BuildTime: "2026-04-25T00:00:00Z", Tag: "dev"},
			wantUpdate:   false,
			wantLatest:   "dev-0042-20260425-bbbbbbb",
		},
		{
			name:         "github direct dev reads pinned dev metadata",
			cfg:          Config{Channel: "dev", Repo: "owner/repo"},
			localVersion: "dev-0007-20260401-aaaaaaa",
			localCommit:  "aaaaaaa",
			remote:       releaseVersionInfo{Version: "dev-0042-20260425-bbbbbbb", Commit: "bbbbbbb", BuildTime: "2026-04-25T00:00:00Z", Tag: "dev"},
			direct:       true,
			wantUpdate:   true,
			wantLatest:   "dev-0042-20260425-bbbbbbb",
		},
		{
			name:         "github direct dev skips same commit",
			cfg:          Config{Channel: "dev", Repo: "owner/repo"},
			localVersion: "dev-0042-20260425-bbbbbbb",
			localCommit:  "bbbbbbb",
			remote:       releaseVersionInfo{Version: "dev-0042-20260425-bbbbbbb", Commit: "bbbbbbb", BuildTime: "2026-04-25T00:00:00Z", Tag: "dev"},
			direct:       true,
			wantUpdate:   false,
			wantLatest:   "dev-0042-20260425-bbbbbbb",
		},
		{
			name:         "github direct stable reads latest metadata",
			cfg:          Config{Channel: "stable", Repo: "owner/repo"},
			localVersion: "v1.0.0",
			remote:       releaseVersionInfo{Version: "v1.4.0", Commit: "bbbbbbb", BuildTime: "2026-04-25T00:00:00Z", Tag: "v1.4.0"},
			direct:       true,
			wantUpdate:   true,
			wantLatest:   "v1.4.0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setTestVersion(t, tc.localVersion, tc.localCommit)
			cfg := tc.cfg
			if tc.direct {
				metadata, err := json.Marshal(tc.remote)
				if err != nil {
					t.Fatal(err)
				}
				wantPath := "/owner/repo/releases/latest/download/version.json"
				if tc.cfg.Channel == "dev" {
					wantPath = "/owner/repo/releases/download/dev/version.json"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case wantPath:
						_, _ = w.Write(metadata)
					default:
						t.Errorf("unexpected path: %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				setTestGitHubBaseURL(t, server.URL)
			} else {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/releases/owner/repo/latest":
						_ = json.NewEncoder(w).Encode(releaseInfo{TagName: tc.remote.Tag, Assets: []assetInfo{}})
					case "/api/releases/owner/repo/dev":
						_ = json.NewEncoder(w).Encode(releaseInfo{
							TagName:         "dev",
							TargetCommitish: tc.remote.Commit,
							Prerelease:      true,
							Assets: []assetInfo{{
								Name:               "version.json",
								BrowserDownloadURL: "https://github.com/owner/repo/releases/download/dev/version.json",
							}},
						})
					case "/download/owner/repo/dev/version.json":
						_ = json.NewEncoder(w).Encode(tc.remote)
					default:
						t.Errorf("unexpected path: %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				cfg.ProxyBaseURL = server.URL
			}

			result, err := testUpdater(cfg, "").CheckOnly(context.Background())
			if err != nil {
				t.Fatalf("CheckOnly returned error: %v", err)
			}
			if result.HasUpdate != tc.wantUpdate || result.LatestVersion != tc.wantLatest {
				t.Fatalf("CheckOnly = %+v, want update=%v latest=%q", result, tc.wantUpdate, tc.wantLatest)
			}
		})
	}
}

func TestCheckOnlyGitHubDirectNoRelease(t *testing.T) {
	setTestVersion(t, "v1.0.0", "")
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	setTestGitHubBaseURL(t, server.URL)

	result, err := testUpdater(Config{Channel: "stable", Repo: "owner/repo"}, "").CheckOnly(context.Background())
	if err != nil {
		t.Fatalf("CheckOnly returned error: %v", err)
	}
	if result.HasUpdate || result.LatestVersion != "" {
		t.Fatalf("expected no update, got %+v", result)
	}
}

// proxyRelease serves a proxy-mode release whose assets are the given names;
// files maps asset name to body. Any path outside that set fails the test,
// which also catches leftover requests for .sig or per-asset .sha256 files.
func proxyRelease(t *testing.T, channel, tag string, commit string, files map[string][]byte) *httptest.Server {
	t.Helper()
	apiTag := "latest"
	if channel == "dev" {
		apiTag = "dev"
	}
	assets := make([]assetInfo, 0, len(files))
	for name, body := range files {
		assets = append(assets, assetInfo{
			Name:               name,
			BrowserDownloadURL: "https://github.com/owner/repo/releases/download/" + tag + "/" + name,
			Size:               int64(len(body)),
		})
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/releases/owner/repo/"+apiTag {
			_ = json.NewEncoder(w).Encode(releaseInfo{
				TagName:         tag,
				TargetCommitish: commit,
				Prerelease:      channel == "dev",
				Assets:          assets,
			})
			return
		}
		if name, ok := strings.CutPrefix(r.URL.Path, "/download/owner/repo/"+tag+"/"); ok {
			if body, found := files[name]; found {
				_, _ = w.Write(body)
				return
			}
		}
		t.Errorf("unexpected path: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
}

func sha256SumsBody(entries map[string]string) []byte {
	var b strings.Builder
	for name, sum := range entries {
		fmt.Fprintf(&b, "%s  %s\n", sum, name)
	}
	return []byte(b.String())
}

func hexSum(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func TestPerformUpdateDownloadsAndVerifiesPrerelease(t *testing.T) {
	setTestVersion(t, "dev-0007-20260401-aaaaaaa", "aaaaaaa")

	target := targetName()
	binary := []byte("new binary")
	remoteVersion := "dev-0042-20260425-bbbbbbb"
	versionJSON, _ := json.Marshal(releaseVersionInfo{
		Version: remoteVersion, Commit: "bbbbbbb", BuildTime: "2026-04-25T00:00:00Z", Tag: "dev",
	})
	// Multi-platform manifest with look-alike names around the real target,
	// so the test exercises line selection rather than a one-line file.
	sums := sha256SumsBody(map[string]string{
		"firegateway-windows-amd64.exe": hexSum([]byte("windows")),
		"firegateway-darwin-arm64":      hexSum([]byte("darwin")),
		target + "64":                   hexSum([]byte("longer name")),
		target + ".exe.bak":             hexSum([]byte("suffix")),
		target:                          hexSum(binary),
	})

	server := proxyRelease(t, "dev", "dev", "bbbbbbb", map[string][]byte{
		target:         binary,
		SHA256SUMSName: sums,
		"version.json": versionJSON,
	})
	defer server.Close()

	dataDir := t.TempDir()
	u := testUpdater(Config{Channel: "dev", Source: "proxy", Repo: "owner/repo", ProxyBaseURL: server.URL}, dataDir)
	u.performUpdate(context.Background())

	status := u.Status()
	if status.State != "ready" {
		t.Fatalf("expected update to be ready, got %q: %s", status.State, status.Error)
	}
	if status.LatestVersion != remoteVersion || u.pendingTag != "dev" {
		t.Fatalf("expected latest %q pending tag dev, got status=%q pending=%q", remoteVersion, status.LatestVersion, u.pendingTag)
	}
	if status.Progress != progressVerifyDone {
		t.Fatalf("expected overall progress %d, got %.0f", progressVerifyDone, status.Progress)
	}
	if want := filepath.Join(dataDir, "updates", "firegateway-dev"); !strings.HasPrefix(u.pendingBinaryPath, want) {
		t.Fatalf("pending path = %q, want prefix %q", u.pendingBinaryPath, want)
	}
	got, err := os.ReadFile(u.pendingBinaryPath)
	if err != nil {
		t.Fatalf("read pending binary: %v", err)
	}
	if string(got) != string(binary) {
		t.Fatalf("pending binary content mismatch")
	}

	// A second run must not re-download while an update is pending.
	u.performUpdate(context.Background())
	if u.Status().State != "ready" {
		t.Fatalf("expected state to stay ready")
	}

	pendingPath := u.pendingBinaryPath
	u.DismissPending()
	if u.Status().State != "idle" {
		t.Fatalf("expected idle after dismiss, got %q", u.Status().State)
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("expected pending binary to be removed on dismiss, stat err = %v", err)
	}
}

// TestPerformUpdateGitHubDirectStable walks the direct-source stable path end
// to end: version.json from the latest redirect, binary and SHA256SUMS from
// the pinned tag, then auto-apply, which is stopped by BeforeExec.
func TestPerformUpdateGitHubDirectStable(t *testing.T) {
	setTestVersion(t, "v1.0.0", "")

	target := targetName()
	binary := []byte("stable binary")
	metadata, _ := json.Marshal(releaseVersionInfo{Version: "v1.4.0", Commit: "bbbbbbb", Tag: "v1.4.0"})
	sums := sha256SumsBody(map[string]string{target: hexSum(binary), "firegateway-linux-riscv64": hexSum([]byte("x"))})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/owner/repo/releases/latest/download/version.json":
			_, _ = w.Write(metadata)
		case "/owner/repo/releases/download/v1.4.0/" + target:
			_, _ = w.Write(binary)
		case "/owner/repo/releases/download/v1.4.0/" + SHA256SUMSName:
			_, _ = w.Write(sums)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	setTestGitHubBaseURL(t, server.URL)

	var gotTag string
	var execErr error
	u := testUpdater(Config{Channel: "stable", Repo: "owner/repo"}, t.TempDir())
	u.hooks.BeforeExec = func(tag string) error {
		gotTag = tag
		return fmt.Errorf("test stops before exec")
	}
	u.hooks.OnExecFailure = func(err error) { execErr = err }

	u.performUpdate(context.Background())

	status := u.Status()
	if gotTag != "v1.4.0" {
		t.Fatalf("BeforeExec tag = %q, want v1.4.0 (status %+v)", gotTag, status)
	}
	if status.State != "failed" || !strings.Contains(status.Error, "prepare restart") {
		t.Fatalf("expected apply to stop at BeforeExec, got %+v", status)
	}
	if execErr == nil {
		t.Fatalf("expected OnExecFailure to be called")
	}
}

func TestPerformUpdateRejectsUnverifiableReleases(t *testing.T) {
	target := targetName()
	binary := []byte("new binary")

	cases := []struct {
		name      string
		files     map[string][]byte
		wantError string
	}{
		{
			name:      "missing SHA256SUMS",
			files:     map[string][]byte{target: binary},
			wantError: "sha256",
		},
		{
			name: "SHA256SUMS without platform entry",
			files: map[string][]byte{
				target: binary,
				SHA256SUMSName: sha256SumsBody(map[string]string{
					"firegateway-other-os": hexSum(binary),
					target + "64":          hexSum(binary),
				}),
			},
			wantError: "no sha256 entry",
		},
		{
			name: "hash mismatch",
			files: map[string][]byte{
				target:         binary,
				SHA256SUMSName: sha256SumsBody(map[string]string{target: hexSum([]byte("something else"))}),
			},
			wantError: "sha256 mismatch",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setTestVersion(t, "v1.0.0", "")
			server := proxyRelease(t, "stable", "v1.4.0", "", tc.files)
			defer server.Close()

			dataDir := t.TempDir()
			u := testUpdater(Config{Channel: "stable", Source: "proxy", Repo: "owner/repo", ProxyBaseURL: server.URL}, dataDir)
			u.hooks.BeforeExec = func(string) error {
				t.Fatalf("unverified update must never reach apply")
				return nil
			}
			u.performUpdate(context.Background())

			status := u.Status()
			if status.State != "failed" {
				t.Fatalf("expected failed, got %q", status.State)
			}
			if !strings.Contains(status.Error, tc.wantError) {
				t.Fatalf("expected error containing %q, got %q", tc.wantError, status.Error)
			}
			entries, _ := os.ReadDir(filepath.Join(dataDir, "updates"))
			if len(entries) != 0 {
				t.Fatalf("expected updates dir to be empty after rejection, found %d entries", len(entries))
			}
		})
	}
}

// TestSHA256ForTargetExactMatch is the prefix-confusion regression: arm must
// not pick up the arm64 line and vice versa.
func TestSHA256ForTargetExactMatch(t *testing.T) {
	sums := []byte(strings.Join([]string{
		"",
		"# comment line",
		"1111111111111111111111111111111111111111111111111111111111111111  firegateway-linux-arm64",
		"2222222222222222222222222222222222222222222222222222222222222222  firegateway-linux-arm",
		"3333333333333333333333333333333333333333333333333333333333333333 *firegateway-windows-amd64.exe",
		"",
	}, "\n"))

	cases := map[string]string{
		"firegateway-linux-arm64":       strings.Repeat("1", 64),
		"firegateway-linux-arm":         strings.Repeat("2", 64),
		"firegateway-windows-amd64.exe": strings.Repeat("3", 64),
	}
	for target, want := range cases {
		got, err := sha256ForTarget(sums, target)
		if err != nil || got != want {
			t.Fatalf("sha256ForTarget(%q) = %q, %v; want %q", target, got, err, want)
		}
	}
	for _, target := range []string{"firegateway-linux-ar", "firegateway-linux", "firegateway-windows-amd64", "firegateway-linux-arm64.exe"} {
		if got, err := sha256ForTarget(sums, target); err == nil {
			t.Fatalf("sha256ForTarget(%q) = %q, want no entry", target, got)
		}
	}
}

func TestApplyPendingMovesToApplyingBeforeAsyncRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	u := testUpdater(Config{}, "")
	u.bgCtx = ctx
	u.hooks.BeforeExec = func(tag string) error {
		return context.Canceled
	}
	u.status.State = "ready"
	u.pendingBinaryPath = filepath.Join(t.TempDir(), "firegateway-new")
	u.pendingTag = "v1.2.0"

	if err := u.ApplyPending(context.Background()); err != nil {
		t.Fatalf("ApplyPending returned error: %v", err)
	}

	status := u.Status()
	if status.State != "applying" {
		t.Fatalf("expected state applying immediately, got %q", status.State)
	}
	if status.Progress != progressApplying {
		t.Fatalf("expected applying progress %d, got %.0f", progressApplying, status.Progress)
	}
	u.mu.RLock()
	consumed := u.pendingBinaryPath == "" && u.pendingTag == ""
	u.mu.RUnlock()
	if !consumed {
		t.Fatalf("expected pending update to be consumed")
	}

	err := u.ApplyPending(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no pending update") {
		t.Fatalf("expected duplicate apply to be rejected, got %v", err)
	}

	// Let the async apply goroutine hit BeforeExec and record the failure.
	deadline := time.Now().Add(2 * time.Second)
	for u.Status().State != "failed" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s := u.Status(); s.State != "failed" || !strings.Contains(s.Error, "prepare restart") {
		t.Fatalf("expected apply to fail at BeforeExec, got %+v", s)
	}
}

func TestApplyPendingWithoutPendingUpdate(t *testing.T) {
	u := testUpdater(Config{}, "")
	if err := u.ApplyPending(context.Background()); err == nil || !strings.Contains(err.Error(), "no pending update") {
		t.Fatalf("expected no pending update error, got %v", err)
	}
}

func TestWaitForIdleStopsWhenApplicationContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	u := testUpdater(Config{}, "")
	u.hooks.IsBusy = func() bool { return true }

	if err := u.waitForIdle(ctx); err != context.Canceled {
		t.Fatalf("waitForIdle error = %v, want context.Canceled", err)
	}
}

func TestStartBackgroundStoresContextWhenDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := testUpdater(Config{Enabled: false}, "")
	u.StartBackground(ctx)
	if u.bgContext() != ctx {
		t.Fatalf("expected background context to be stored even when disabled")
	}
}

func TestNormalizeConfigDefaults(t *testing.T) {
	got := normalizeConfig(Config{Channel: " DEV ", Source: "Proxy", ProxyBaseURL: "https://mirror.example/ "})
	if got.Channel != "dev" || got.Source != SourceProxy || got.ProxyBaseURL != "https://mirror.example" {
		t.Fatalf("unexpected normalization: %+v", got)
	}
	got = normalizeConfig(Config{})
	want := Config{Channel: "stable", CheckInterval: 3600, Source: SourceGitHub, ProxyBaseURL: "https://dl.repo.chycloud.top", Repo: "lieyanc/FireGateway"}
	if got != want {
		t.Fatalf("normalizeConfig(Config{}) = %+v, want %+v", got, want)
	}
	if got := normalizeConfig(Config{Channel: "nightly"}).Channel; got != "dev" {
		t.Fatalf("unknown channel normalized to %q, want dev", got)
	}
}

func TestResolveDownloadURL(t *testing.T) {
	u := testUpdater(Config{}, "")
	src := "https://github.com/owner/repo/releases/download/v1.0.0/firegateway-linux-amd64"
	proxy := Config{Source: SourceProxy, ProxyBaseURL: "https://mirror.example"}
	if got := u.resolveDownloadURL(proxy, src); got != "https://mirror.example/download/owner/repo/v1.0.0/firegateway-linux-amd64" {
		t.Fatalf("proxy URL = %q", got)
	}
	if got := u.resolveDownloadURL(Config{Source: SourceGitHub}, src); got != src {
		t.Fatalf("direct URL = %q, want unchanged", got)
	}
}

func TestIsNewerVersionComparison(t *testing.T) {
	u := testUpdater(Config{}, "")
	cases := []struct {
		local, commit string
		release       releaseInfo
		channel       string
		want          bool
	}{
		{"dev", "", releaseInfo{TagName: "v0.0.1"}, "stable", true},
		{"v1.2.3", "", releaseInfo{TagName: "v1.10.0"}, "stable", true},
		{"v1.10.0", "", releaseInfo{TagName: "v1.9.9"}, "stable", false},
		{"v1.2.3", "", releaseInfo{TagName: "v1.2.3"}, "stable", false},
		// Dev fallback on run numbers when commits are unknown.
		{"dev-0010-20260101-aaaaaaa", "unknown", releaseInfo{TagName: "dev", Version: "dev-0011-20260102-bbbbbbb"}, "dev", true},
		{"dev-0010-20260101-aaaaaaa", "unknown", releaseInfo{TagName: "dev", Version: "dev-0009-20260102-bbbbbbb"}, "dev", false},
		{"dev-0010-20260101-aaaaaaa", "unknown", releaseInfo{TagName: "dev"}, "dev", false},
	}
	for _, tc := range cases {
		setTestVersion(t, tc.local, tc.commit)
		if got := u.isNewer(tc.release, tc.channel); got != tc.want {
			t.Errorf("isNewer(local=%s, remote=%+v, %s) = %v, want %v", tc.local, tc.release, tc.channel, got, tc.want)
		}
	}
}

func setTestVersion(t *testing.T, ver, commit string) {
	t.Helper()
	originalVersion, originalCommit := version.Version, version.Commit
	version.Version = ver
	if commit != "" {
		version.Commit = commit
	}
	t.Cleanup(func() { version.Version, version.Commit = originalVersion, originalCommit })
}

func setTestGitHubBaseURL(t *testing.T, url string) {
	t.Helper()
	original := githubBaseURL
	githubBaseURL = url
	t.Cleanup(func() { githubBaseURL = original })
}

func testUpdater(cfg Config, dataDir string) *Updater {
	return New(
		func() Config { return cfg },
		func() string { return dataDir },
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		RestartHooks{},
	)
}
