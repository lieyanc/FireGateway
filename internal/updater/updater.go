// Package updater implements OTA self-update from GitHub releases.
//
// Releases are discovered through version.json (GitHub direct links or a
// proxy mirror), the platform binary is downloaded and verified against the
// release's single SHA256SUMS manifest, and the running process is replaced
// in place. There is no release signing: integrity relies on the sha256
// manifest served over HTTPS from GitHub or a self-hosted mirror.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lieyanc/FireGateway/internal/version"
)

type Config struct {
	Enabled       bool   `json:"enabled"`
	Channel       string `json:"channel"`       // stable | dev
	CheckInterval int    `json:"checkInterval"` // seconds
	Source        string `json:"source"`        // github | proxy
	ProxyBaseURL  string `json:"proxyBaseUrl"`
	Repo          string `json:"repo"` // owner/name
}

type Status struct {
	State            string  `json:"state"`
	CurrentVersion   string  `json:"currentVersion"`
	LatestVersion    string  `json:"latestVersion,omitempty"`
	IsPrerelease     bool    `json:"isPrerelease"`
	Progress         float64 `json:"progress,omitempty"`
	DownloadProgress float64 `json:"downloadProgress,omitempty"`
	Error            string  `json:"error,omitempty"`
	LastCheck        string  `json:"lastCheck,omitempty"`
	ReleaseNotes     string  `json:"releaseNotes,omitempty"`
}

type CheckResult struct {
	HasUpdate      bool   `json:"hasUpdate"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion,omitempty"`
	IsPrerelease   bool   `json:"isPrerelease"`
	ReleaseNotes   string `json:"releaseNotes,omitempty"`
	Channel        string `json:"channel"`
}

type RestartHooks struct {
	// BeforeExec runs right before the process is replaced (close listeners,
	// flush state). Returning an error aborts the apply.
	BeforeExec func(tag string) error
	// OnExecFailure is called when applying or exec'ing the new binary fails.
	OnExecFailure func(error)
	// IsBusy reports whether the application is processing work that should
	// not be interrupted by a restart. When set, the updater waits for it to
	// return false (bounded by idleWaitTimeout) before applying an update.
	IsBusy func() bool
}

type Updater struct {
	cfg     func() Config
	dataDir func() string
	logger  *slog.Logger
	hooks   RestartHooks

	mu     sync.RWMutex
	status Status

	bgCtx context.Context

	pendingBinaryPath string
	pendingTag        string
}

const (
	appName = "firegateway"

	// SHA256SUMSName is the single checksum manifest published with every
	// release, in native sha256sum format: "<hash>  <bare file name>".
	SHA256SUMSName = "SHA256SUMS"

	defaultChannel       = "stable"
	defaultCheckInterval = 3600
	defaultProxyBaseURL  = "https://dl.repo.chycloud.top"
	defaultRepo          = "lieyanc/FireGateway"

	startupDelay     = 30 * time.Second
	minCheckInterval = time.Minute
	checkTimeout     = 30 * time.Second
	idleWaitTimeout  = 10 * time.Minute
	idlePollInterval = 5 * time.Second
	downloadTimeout  = 30 * time.Minute

	// SourceGitHub downloads release assets straight from
	// github.com/<repo>/releases/download/... links, which are CDN
	// redirects and not subject to GitHub REST API rate limits.
	SourceGitHub = "github"
	// SourceProxy routes release lookups and downloads through the
	// configured ProxyBaseURL mirror.
	SourceProxy = "proxy"

	progressChecking      = 5
	progressReleaseFound  = 10
	progressDownloadStart = 10
	progressDownloadDone  = 90
	progressVerifyStart   = 92
	progressVerifyDone    = 95
	progressApplying      = 98
	progressComplete      = 100
)

// New creates an updater. cfg and dataDir are called on every use, so config
// changes at runtime are picked up without recreating the updater.
func New(cfg func() Config, dataDir func() string, logger *slog.Logger, hooks RestartHooks) *Updater {
	if logger == nil {
		logger = slog.Default()
	}
	return &Updater{
		cfg:     cfg,
		dataDir: dataDir,
		logger:  logger,
		hooks:   hooks,
		status: Status{
			State:          "idle",
			CurrentVersion: version.Version,
		},
	}
}

func (u *Updater) Status() Status {
	u.mu.RLock()
	defer u.mu.RUnlock()
	s := u.status
	s.CurrentVersion = version.Version
	return s
}

// CheckOnly looks up the latest release for the configured channel without
// downloading anything.
func (u *Updater) CheckOnly(ctx context.Context) (CheckResult, error) {
	cfg := normalizeConfig(u.cfg())
	result := CheckResult{
		CurrentVersion: version.Version,
		Channel:        cfg.Channel,
	}

	release, hasUpdate, err := u.checkForUpdate(ctx, cfg)
	if err != nil {
		return result, err
	}

	u.mu.Lock()
	u.status.LastCheck = time.Now().UTC().Format(time.RFC3339)
	u.mu.Unlock()

	if release == nil {
		return result, nil
	}

	result.HasUpdate = hasUpdate
	result.LatestVersion = release.displayVersion()
	result.IsPrerelease = release.Prerelease
	result.ReleaseNotes = release.Body

	u.mu.Lock()
	u.status.LatestVersion = release.displayVersion()
	u.status.IsPrerelease = release.Prerelease
	u.status.ReleaseNotes = release.Body
	u.mu.Unlock()

	return result, nil
}

// StartUpdate runs check → download (→ apply on stable) in the background.
// The passed context is ignored on purpose: a request context would be
// canceled as soon as the HTTP response is written, so the long-lived
// context from StartBackground is used instead.
func (u *Updater) StartUpdate(_ context.Context) {
	go u.performUpdate(u.bgContext())
}

// ApplyPending installs a downloaded dev-channel update that is waiting in
// the "ready" state and restarts the process.
func (u *Updater) ApplyPending(_ context.Context) error {
	u.mu.Lock()
	state := u.status.State
	path := u.pendingBinaryPath
	tag := u.pendingTag

	if state != "ready" || path == "" {
		u.mu.Unlock()
		return fmt.Errorf("no pending update to apply")
	}

	u.status.State = "applying"
	u.status.Progress = progressApplying
	u.status.DownloadProgress = 0
	u.pendingBinaryPath = ""
	u.pendingTag = ""
	u.mu.Unlock()

	go func() {
		// Let the HTTP response go out before the process is replaced.
		time.Sleep(200 * time.Millisecond)
		if err := u.waitForIdle(u.bgContext()); err != nil {
			u.setError("apply canceled while waiting for idle: " + err.Error())
			return
		}
		if err := u.applyUpdate(path, tag); err != nil {
			u.notifyExecFailure(err)
			u.setError("apply failed: " + err.Error())
		}
	}()
	return nil
}

// DismissPending discards a downloaded update waiting in the "ready" state.
func (u *Updater) DismissPending() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.status.State == "ready" {
		if u.pendingBinaryPath != "" {
			_ = os.Remove(u.pendingBinaryPath)
		}
		u.pendingBinaryPath = ""
		u.pendingTag = ""
		u.status.State = "idle"
		u.status.LatestVersion = ""
		u.status.Progress = 0
		u.status.DownloadProgress = 0
		u.status.Error = ""
	}
}

// StartBackground stores ctx as the long-lived context for manual updates
// and starts the periodic check loop. The loop always runs and re-reads
// Enabled on every tick, so enabling updates at runtime takes effect without
// a restart.
func (u *Updater) StartBackground(ctx context.Context) {
	u.mu.Lock()
	u.bgCtx = ctx
	u.mu.Unlock()

	cfg := normalizeConfig(u.cfg())
	if cfg.Enabled {
		u.logger.Info("update: automatic checks enabled", "channel", cfg.Channel, "source", cfg.Source, "interval", cfg.CheckInterval)
	} else {
		u.logger.Info("update: automatic checks disabled")
	}
	go u.loop(ctx)
}

func (u *Updater) bgContext() context.Context {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.bgCtx != nil {
		return u.bgCtx
	}
	return context.Background()
}

func (u *Updater) loop(ctx context.Context) {
	select {
	case <-time.After(startupDelay):
	case <-ctx.Done():
		return
	}

	u.checkAndUpdate(ctx)

	for {
		cfg := normalizeConfig(u.cfg())
		interval := time.Duration(cfg.CheckInterval) * time.Second
		if interval < minCheckInterval {
			interval = minCheckInterval
		}
		select {
		case <-time.After(interval):
			u.checkAndUpdate(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (u *Updater) checkAndUpdate(ctx context.Context) {
	cfg := normalizeConfig(u.cfg())
	if !cfg.Enabled {
		return
	}
	u.performUpdate(ctx)
}

func (u *Updater) performUpdate(ctx context.Context) {
	cfg := normalizeConfig(u.cfg())

	u.mu.Lock()
	switch u.status.State {
	case "checking", "ready", "downloading", "applying":
		u.mu.Unlock()
		return
	}
	u.status.State = "checking"
	u.status.Progress = progressChecking
	u.status.Error = ""
	u.status.DownloadProgress = 0
	u.mu.Unlock()

	release, hasUpdate, err := u.checkForUpdate(ctx, cfg)
	if err != nil {
		u.setError("check failed: " + err.Error())
		return
	}
	if release == nil || !hasUpdate {
		u.mu.Lock()
		u.status.State = "idle"
		u.status.Progress = 0
		u.status.DownloadProgress = 0
		u.status.LastCheck = time.Now().UTC().Format(time.RFC3339)
		u.mu.Unlock()
		return
	}

	u.mu.Lock()
	u.status.LatestVersion = release.displayVersion()
	u.status.IsPrerelease = release.Prerelease
	u.status.ReleaseNotes = release.Body
	u.status.LastCheck = time.Now().UTC().Format(time.RFC3339)
	u.status.Progress = progressReleaseFound
	u.mu.Unlock()

	binaryPath, err := u.download(ctx, cfg, release)
	if err != nil {
		u.setError("download failed: " + err.Error())
		return
	}

	// Stable means the user opted into automatic updates to releases; dev
	// builds wait in "ready" for explicit confirmation.
	if cfg.Channel == "stable" {
		u.mu.Lock()
		u.status.State = "applying"
		u.status.Progress = progressApplying
		u.status.DownloadProgress = 0
		u.mu.Unlock()
		if err := u.waitForIdle(ctx); err != nil {
			u.setError("apply canceled while waiting for idle: " + err.Error())
			return
		}
		if err := u.applyUpdate(binaryPath, release.TagName); err != nil {
			u.notifyExecFailure(err)
			u.setError("apply failed: " + err.Error())
		}
		return
	}

	u.mu.Lock()
	u.status.State = "ready"
	u.status.Progress = progressVerifyDone
	u.status.DownloadProgress = 0
	u.pendingBinaryPath = binaryPath
	u.pendingTag = release.TagName
	u.mu.Unlock()
	u.logger.Info("update: pre-release ready, waiting for confirmation", "tag", release.TagName, "version", release.displayVersion())
}

func (u *Updater) setError(msg string) {
	u.logger.Warn("update: " + msg)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status.State = "failed"
	u.status.Error = msg
	u.status.LastCheck = time.Now().UTC().Format(time.RFC3339)
}

func clampProgress(progress float64) float64 {
	if progress < 0 {
		return 0
	}
	if progress > 100 {
		return 100
	}
	return progress
}

func overallDownloadProgress(downloadProgress float64) float64 {
	downloadProgress = clampProgress(downloadProgress)
	span := progressDownloadDone - progressDownloadStart
	return progressDownloadStart + downloadProgress*float64(span)/100
}

func (u *Updater) notifyExecFailure(err error) {
	if err == nil || u.hooks.OnExecFailure == nil {
		return
	}
	u.hooks.OnExecFailure(err)
}

// waitForIdle blocks until the application reports no in-flight work. A
// canceled application context aborts the update; only the deliberate idle
// timeout permits applying while work is still reported as active.
func (u *Updater) waitForIdle(ctx context.Context) error {
	if u.hooks.IsBusy == nil || !u.hooks.IsBusy() {
		return nil
	}
	u.logger.Info("update: waiting for in-flight work before applying", "maxWait", idleWaitTimeout.String())
	deadline := time.After(idleWaitTimeout)
	ticker := time.NewTicker(idlePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			u.logger.Warn("update: idle wait timed out, applying anyway")
			return nil
		case <-ticker.C:
			if !u.hooks.IsBusy() {
				return nil
			}
		}
	}
}

// releaseInfo mirrors the GitHub release JSON served by the proxy mirror,
// so its tags follow GitHub's snake_case.
type releaseInfo struct {
	TagName         string      `json:"tag_name"`
	TargetCommitish string      `json:"target_commitish"`
	Prerelease      bool        `json:"prerelease"`
	Body            string      `json:"body"`
	Assets          []assetInfo `json:"assets"`
	Version         string      `json:"-"`
	Commit          string      `json:"-"`
	BuildTime       string      `json:"-"`
}

type assetInfo struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// releaseVersionInfo is the version.json release asset written by CI
// (.github/workflows/release.yml). Its field names are part of the release
// protocol; change both sides together.
type releaseVersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	Tag       string `json:"tag"`
}

func (r releaseInfo) displayVersion() string {
	if strings.TrimSpace(r.Version) != "" {
		return strings.TrimSpace(r.Version)
	}
	return r.TagName
}

// githubBaseURL is a var so tests can point direct-source checks at a local
// server.
var githubBaseURL = "https://github.com"

func (u *Updater) checkForUpdate(ctx context.Context, cfg Config) (*releaseInfo, bool, error) {
	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	var (
		release *releaseInfo
		err     error
	)
	if cfg.Source == SourceProxy {
		release, err = u.fetchReleaseViaProxy(checkCtx, cfg)
	} else {
		release, err = u.fetchReleaseFromGitHub(checkCtx, cfg)
	}
	if err != nil || release == nil {
		return nil, false, err
	}
	if !u.isNewer(*release, cfg.Channel) {
		u.logger.Info("update: already up to date", "latest", release.displayVersion(), "current", version.Version)
		return release, false, nil
	}
	return release, true, nil
}

// fetchReleaseFromGitHub resolves the latest release without touching the
// GitHub REST API: it fetches version.json straight from the release
// download URL (fixed "dev" tag, or the "latest" redirect for stable) and
// synthesizes the asset list from the tag it names. version.json only picks
// the tag; the downloaded binary is still checked against SHA256SUMS.
func (u *Updater) fetchReleaseFromGitHub(ctx context.Context, cfg Config) (*releaseInfo, error) {
	base := githubBaseURL + "/" + cfg.Repo + "/releases"
	versionURL := base + "/latest/download/version.json"
	if cfg.Channel != "stable" {
		versionURL = base + "/download/dev/version.json"
	}
	u.logger.Info("update check", "url", versionURL)

	body, status, err := u.httpGet(ctx, versionURL, 16*1024)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		u.logger.Info("update: no release found", "channel", cfg.Channel)
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", status)
	}

	var info releaseVersionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("decode version metadata: %w", err)
	}
	tag := strings.TrimSpace(info.Tag)
	if cfg.Channel != "stable" {
		tag = "dev"
	} else if tag == "" {
		return nil, fmt.Errorf("version metadata missing release tag")
	}

	assetNames := []string{targetName(), SHA256SUMSName, "version.json"}
	assets := make([]assetInfo, 0, len(assetNames))
	for _, name := range assetNames {
		assets = append(assets, assetInfo{
			Name:               name,
			BrowserDownloadURL: base + "/download/" + tag + "/" + name,
		})
	}
	return &releaseInfo{
		TagName:    tag,
		Prerelease: cfg.Channel != "stable",
		Assets:     assets,
		Version:    strings.TrimSpace(info.Version),
		Commit:     strings.TrimSpace(info.Commit),
		BuildTime:  strings.TrimSpace(info.BuildTime),
	}, nil
}

func (u *Updater) fetchReleaseViaProxy(ctx context.Context, cfg Config) (*releaseInfo, error) {
	tag := "latest"
	if cfg.Channel != "stable" {
		tag = "dev"
	}

	url := fmt.Sprintf("%s/api/releases/%s/%s", cfg.ProxyBaseURL, cfg.Repo, tag)
	u.logger.Info("update check", "url", url)

	body, status, err := u.httpGet(ctx, url, 1024*1024)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		u.logger.Info("update: no release found", "channel", cfg.Channel)
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", status)
	}

	var release releaseInfo
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	if cfg.Channel != "stable" {
		// Only metadata: the dev tag name alone cannot be compared, but a
		// failure here still leaves TargetCommitish as a fallback.
		if err := u.loadReleaseVersion(ctx, cfg, &release); err != nil {
			u.logger.Warn("update: version metadata unavailable", "tag", release.TagName, "err", err)
		}
	}
	return &release, nil
}

func (u *Updater) loadReleaseVersion(ctx context.Context, cfg Config, release *releaseInfo) error {
	versionAsset := findAsset(release.Assets, "version.json")
	if versionAsset == nil {
		return fmt.Errorf("version.json asset not found")
	}

	body, err := u.fetchAsset(ctx, cfg, versionAsset, 16*1024)
	if err != nil {
		return err
	}
	var info releaseVersionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return fmt.Errorf("decode version metadata: %w", err)
	}
	release.Version = strings.TrimSpace(info.Version)
	release.Commit = strings.TrimSpace(info.Commit)
	release.BuildTime = strings.TrimSpace(info.BuildTime)
	return nil
}

func findAsset(assets []assetInfo, name string) *assetInfo {
	for i := range assets {
		if assets[i].Name == name {
			return &assets[i]
		}
	}
	return nil
}

func (u *Updater) isNewer(release releaseInfo, channel string) bool {
	current := version.Version
	if current == "dev" {
		// Local unversioned builds always see an update.
		return true
	}
	remoteTag := release.TagName
	if channel == "stable" {
		return semverGreater(remoteTag, current)
	}

	// Dev channel: a different commit means an update, regardless of order,
	// because run numbers are unreliable after reruns and force-pushes.
	remoteCommit := normalizeCommit(release.Commit)
	if remoteCommit == "" {
		remoteCommit = normalizeCommit(release.TargetCommitish)
	}
	currentCommit := normalizeCommit(version.Commit)
	if remoteCommit != "" && currentCommit != "" {
		return remoteCommit != currentCommit
	}

	remoteVersion := release.displayVersion()
	if remoteTag == "dev" && remoteVersion == "dev" {
		u.logger.Warn("update: dev release missing comparable commit, skipping", "current", current, "remote", remoteTag)
		return false
	}

	remoteNum, remoteSHA := parseDevTag(remoteVersion)
	localNum, localSHA := parseDevTag(current)
	if remoteSHA != "" && localSHA != "" && remoteSHA == localSHA {
		return false
	}
	if remoteNum > 0 && localNum > 0 {
		return remoteNum > localNum
	}
	// Never guess: a wrong "newer" answer would loop self-updates forever.
	u.logger.Warn("update: cannot compare versions, skipping", "current", current, "remote", remoteVersion)
	return false
}

func normalizeCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if commit == "" || commit == "unknown" {
		return ""
	}
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

func semverGreater(a, b string) bool {
	av := parseSemver(strings.TrimPrefix(a, "v"))
	bv := parseSemver(strings.TrimPrefix(b, "v"))
	for i := 0; i < 3; i++ {
		if av[i] > bv[i] {
			return true
		}
		if av[i] < bv[i] {
			return false
		}
	}
	return false
}

func parseSemver(s string) [3]int {
	var result [3]int
	parts := strings.SplitN(s, ".", 3)
	for i, p := range parts {
		if idx := strings.IndexByte(p, '-'); idx >= 0 {
			p = p[:idx]
		}
		n, _ := strconv.Atoi(p)
		result[i] = n
	}
	return result
}

// parseDevTag splits the CI dev version format dev-{run:%04d}-{yyyymmdd}-{sha}.
func parseDevTag(tag string) (runNumber int, sha string) {
	parts := strings.SplitN(tag, "-", 4)
	if len(parts) >= 4 && parts[0] == "dev" {
		n, _ := strconv.Atoi(parts[1])
		return n, parts[3]
	}
	return 0, ""
}

// targetName is the release asset name for this platform. It must match the
// CI build matrix target names exactly: firegateway-{goos}-{goarch}{.exe}.
func targetName() string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	return appName + "-" + runtime.GOOS + "-" + runtime.GOARCH + ext
}

func (u *Updater) download(ctx context.Context, cfg Config, release *releaseInfo) (string, error) {
	u.mu.Lock()
	u.status.State = "downloading"
	u.status.Progress = progressDownloadStart
	u.status.DownloadProgress = 0
	u.mu.Unlock()

	target := targetName()
	binaryAsset := findAsset(release.Assets, target)
	sumsAsset := findAsset(release.Assets, SHA256SUMSName)
	if binaryAsset == nil {
		return "", fmt.Errorf("no asset found for %s in release %s", target, release.TagName)
	}
	if sumsAsset == nil {
		return "", fmt.Errorf("release %s is missing %s, refusing unverified update (no sha256 manifest)", release.TagName, SHA256SUMSName)
	}

	updateDir := filepath.Join(u.dataDir(), "updates")
	if err := os.MkdirAll(updateDir, 0o755); err != nil {
		return "", fmt.Errorf("create update dir: %w", err)
	}

	finalName := appName + "-" + sanitizePathPart(release.TagName)
	if runtime.GOOS == "windows" {
		finalName += ".exe"
	}
	tmpPath := filepath.Join(updateDir, finalName+".tmp")
	finalPath := filepath.Join(updateDir, finalName)

	dlCtx, cancelDownload := context.WithTimeout(ctx, downloadTimeout)
	defer cancelDownload()

	downloadURL := u.resolveDownloadURL(cfg, binaryAsset.BrowserDownloadURL)
	u.logger.Info("update download", "tag", release.TagName, "url", downloadURL)
	if err := u.downloadFile(dlCtx, downloadURL, tmpPath, binaryAsset.Size); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("download binary: %w", err)
	}

	u.mu.Lock()
	u.status.Progress = progressVerifyStart
	u.mu.Unlock()

	sums, err := u.fetchAsset(dlCtx, cfg, sumsAsset, 64*1024)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("fetch %s: %w", SHA256SUMSName, err)
	}
	expectedHash, err := sha256ForTarget(sums, target)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	actualHash, err := fileSHA256(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("compute sha256: %w", err)
	}
	if !strings.EqualFold(actualHash, expectedHash) {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("sha256 mismatch: expected %s, got %s", expectedHash, actualHash)
	}
	u.logger.Info("update: sha256 verified", "tag", release.TagName, "sha256", actualHash)

	u.mu.Lock()
	u.status.Progress = progressVerifyDone
	u.mu.Unlock()

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}

	u.logger.Info("update: downloaded", "tag", release.TagName, "path", finalPath)
	return finalPath, nil
}

// sha256ForTarget picks the hash for target out of a sha256sum-format
// manifest. Names are matched by exact equality: prefix matching would let
// firegateway-linux-arm pick up the firegateway-linux-arm64 line.
func sha256ForTarget(sums []byte, target string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*") // binary-mode marker
		if name == target {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no sha256 entry for %s in %s", target, SHA256SUMSName)
}

// resolveDownloadURL maps a GitHub browser_download_url onto the proxy
// mirror when the proxy source is selected; in direct mode the URL is used
// as-is.
func (u *Updater) resolveDownloadURL(cfg Config, browserURL string) string {
	if cfg.Source != SourceProxy {
		return browserURL
	}
	const ghPrefix = "https://github.com/"
	if !strings.HasPrefix(browserURL, ghPrefix) {
		return browserURL
	}
	path := strings.TrimPrefix(browserURL, ghPrefix)
	const relSegment = "/releases/download/"
	idx := strings.Index(path, relSegment)
	if idx < 0 {
		return browserURL
	}
	ownerRepo := path[:idx]
	tagAndAsset := path[idx+len(relSegment):]
	return cfg.ProxyBaseURL + "/download/" + ownerRepo + "/" + tagAndAsset
}

func (u *Updater) downloadFile(ctx context.Context, url, destPath string, expectedSize int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	totalSize := resp.ContentLength
	if totalSize <= 0 && expectedSize > 0 {
		totalSize = expectedSize
	}

	var written int64
	var lastProgress float64
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := f.Write(buf[:n]); wErr != nil {
				return wErr
			}
			written += int64(n)
			if totalSize > 0 {
				progress := float64(written) / float64(totalSize) * 100
				if progress-lastProgress >= 1 || progress >= 100 {
					u.mu.Lock()
					u.status.DownloadProgress = clampProgress(progress)
					u.status.Progress = overallDownloadProgress(progress)
					u.mu.Unlock()
					lastProgress = progress
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return readErr
		}
	}

	u.mu.Lock()
	u.status.DownloadProgress = progressComplete
	u.status.Progress = overallDownloadProgress(progressComplete)
	u.mu.Unlock()
	return f.Close()
}

// httpGet fetches url and returns up to limit bytes of the body along with
// the status code. Network errors are returned; HTTP error statuses are not.
func (u *Updater) httpGet(ctx context.Context, url string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func (u *Updater) fetchAsset(ctx context.Context, cfg Config, asset *assetInfo, limit int64) ([]byte, error) {
	body, status, err := u.httpGet(ctx, u.resolveDownloadURL(cfg, asset.BrowserDownloadURL), limit)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s returned status %d", asset.Name, status)
	}
	return body, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (u *Updater) applyUpdate(newBinaryPath, tag string) error {
	u.mu.Lock()
	u.status.State = "applying"
	u.status.Progress = progressApplying
	u.mu.Unlock()

	if runtime.GOOS == "windows" {
		return u.applyUpdateWindows(newBinaryPath, tag)
	}
	return u.applyUpdateUnix(newBinaryPath, tag)
}

func (u *Updater) applyUpdateUnix(newBinaryPath, tag string) error {
	if u.hooks.BeforeExec != nil {
		if err := u.hooks.BeforeExec(tag); err != nil {
			return fmt.Errorf("prepare restart: %w", err)
		}
	}

	execPath, err := executablePath()
	if err != nil {
		return err
	}

	backupPath := execPath + ".bak"
	if err := os.Rename(execPath, backupPath); err != nil {
		return fmt.Errorf("backup current binary: %w", err)
	}
	if err := copyFile(newBinaryPath, execPath); err != nil {
		_ = os.Rename(backupPath, execPath)
		return fmt.Errorf("install new binary: %w", err)
	}
	if err := os.Chmod(execPath, 0o755); err != nil {
		_ = os.Rename(backupPath, execPath)
		_ = os.Remove(newBinaryPath)
		return fmt.Errorf("chmod new binary: %w", err)
	}

	_ = os.Remove(backupPath)
	_ = os.Remove(newBinaryPath)

	u.logger.Info("update: restarting with new binary", "tag", tag)
	u.mu.Lock()
	u.status.Progress = progressComplete
	u.mu.Unlock()
	return replaceProcess(execPath, os.Args, os.Environ())
}

func (u *Updater) applyUpdateWindows(newBinaryPath, tag string) error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	execPath, err = filepath.Abs(execPath)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	// A running .exe cannot be overwritten, so a detached PowerShell script
	// waits for this process to exit, swaps the binary and starts it again.
	updateDir := filepath.Dir(newBinaryPath)
	scriptPath := filepath.Join(updateDir, "apply-"+sanitizePathPart(tag)+".ps1")
	backupPath := execPath + ".bak"
	script := strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		fmt.Sprintf("$pidToWait = %d", os.Getpid()),
		"$exe = " + psQuote(execPath),
		"$new = " + psQuote(newBinaryPath),
		"$bak = " + psQuote(backupPath),
		"$argsList = " + psArray(os.Args[1:]),
		"$workDir = " + psQuote(cwd),
		"while (Get-Process -Id $pidToWait -ErrorAction SilentlyContinue) { Start-Sleep -Milliseconds 250 }",
		"if (Test-Path $bak) { Remove-Item -Force $bak }",
		"if (Test-Path $exe) { Move-Item -Force $exe $bak }",
		"Copy-Item -Force $new $exe",
		"Remove-Item -Force $new",
		"Start-Process -FilePath $exe -ArgumentList $argsList -WorkingDirectory $workDir",
		"Remove-Item -Force $PSCommandPath",
		"",
	}, "\r\n")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		return fmt.Errorf("write apply script: %w", err)
	}

	if u.hooks.BeforeExec != nil {
		if err := u.hooks.BeforeExec(tag); err != nil {
			return fmt.Errorf("prepare restart: %w", err)
		}
	}

	proc, err := os.StartProcess("powershell.exe", []string{
		"powershell.exe",
		"-NoProfile",
		"-ExecutionPolicy", "Bypass",
		"-File", scriptPath,
	}, &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Env:   os.Environ(),
	})
	if err != nil {
		return fmt.Errorf("start apply script: %w", err)
	}
	_ = proc.Release()

	u.logger.Info("update: restarting with new binary", "tag", tag)
	u.mu.Lock()
	u.status.Progress = progressComplete
	u.mu.Unlock()
	os.Exit(0)
	return nil
}

// Restart re-executes the current binary in place with the same arguments
// and environment: on Unix via exec(2) (the PID is kept), on Windows by
// starting a new process and exiting. beforeExec, if non-nil, runs first
// (close listeners, flush state); an error from it aborts the restart. On
// success Restart does not return.
func Restart(beforeExec func() error) error {
	execPath, err := executablePath()
	if err != nil {
		return err
	}
	if beforeExec != nil {
		if err := beforeExec(); err != nil {
			return fmt.Errorf("prepare restart: %w", err)
		}
	}
	return replaceProcess(execPath, os.Args, os.Environ())
}

func executablePath() (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	return execPath, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func normalizeConfig(cfg Config) Config {
	cfg.Channel = strings.ToLower(strings.TrimSpace(cfg.Channel))
	if cfg.Channel == "" {
		cfg.Channel = defaultChannel
	}
	if cfg.Channel != "stable" {
		cfg.Channel = "dev"
	}
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = defaultCheckInterval
	}
	cfg.Source = strings.ToLower(strings.TrimSpace(cfg.Source))
	if cfg.Source != SourceProxy {
		cfg.Source = SourceGitHub
	}
	cfg.ProxyBaseURL = strings.TrimRight(strings.TrimSpace(cfg.ProxyBaseURL), "/")
	if cfg.ProxyBaseURL == "" {
		cfg.ProxyBaseURL = defaultProxyBaseURL
	}
	cfg.Repo = strings.Trim(strings.TrimSpace(cfg.Repo), "/")
	if cfg.Repo == "" {
		cfg.Repo = defaultRepo
	}
	return cfg
}

func sanitizePathPart(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "update"
	}
	return b.String()
}

func psQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func psArray(values []string) string {
	if len(values) == 0 {
		return "@()"
	}
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, psQuote(value))
	}
	return "@(" + strings.Join(quoted, ", ") + ")"
}
