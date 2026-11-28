package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	updaterAppSlug              = "sumi"
	updaterDefaultRepo          = "abcdlsj/sumi"
	updaterExpectedBundleID     = "com.sumi.desktop"
	updaterExpectedBundleName   = "Sumi.app"
	updaterExpectedExeName      = "Sumi"
	updaterPubKeyB64            = "KeD64Kqwp4x1AlaDealehsIPI9yXFqNTP8XqjdnK2A0="
	updaterDefaultCheckInterval = 6 * time.Hour
	updaterDefaultInitialDelay  = 20 * time.Second
	updaterHandoffTTL           = 120 * time.Second

	updaterManualCheckCooldown = 30 * time.Second
	updaterMaxManifestBytes    = 1 << 20   // 1 MiB
	updaterMaxSigBytes         = 4096      // 4 KiB
	updaterMaxAssetBytes       = 512 << 20 // 512 MiB
	updaterHandoffTTLMS        = int64(updaterHandoffTTL / time.Millisecond)
)

const (
	updateStateIdle           = "idle"
	updateStateChecking       = "checking"
	updateStateAvailable      = "available"
	updateStateDownloading    = "downloading"
	updateStateVerifying      = "verifying"
	updateStateReadyToRestart = "ready_to_restart"
	updateStateInstalling     = "installing"
	updateStateError          = "error"
)

// updateStatus is the JSON snapshot returned by GET /desktop/update.
type updateStatus struct {
	Current    string `json:"current"`
	Latest     string `json:"latest,omitempty"`
	Available  bool   `json:"available"`
	State      string `json:"state"`
	Progress   int    `json:"progress"`
	CanApply   bool   `json:"can_apply"`
	ReleaseURL string `json:"release_url,omitempty"`
	Notes      string `json:"notes,omitempty"`
	Error      string `json:"error,omitempty"`
}

// updateHandoffPayload is persisted atomically as update-handoff.json right
// before restarting into a staged bundle, and consumed once on next startup.
type updateHandoffPayload struct {
	SchemaVersion int            `json:"schema_version"`
	FromVersion   string         `json:"from_version"`
	ToVersion     string         `json:"to_version"`
	CreatedAtMS   int64          `json:"created_at_ms"`
	TTLMS         int64          `json:"ttl_ms"`
	AppState      map[string]any `json:"app_state"`
}

// updaterConfig configures an updaterManager.
type updaterConfig struct {
	Repo           string
	CurrentVersion string
	ManifestURL    string
	PublicKeyB64   string
	CheckInterval  time.Duration
	InitialDelay   time.Duration
	ClientFactory  func(timeout time.Duration) *http.Client
	DataDir        string
	CacheDir       string
	Logger         *slog.Logger
	OnQuit         func()
	EnableApply    bool

	// test hooks; left nil in production.
	detectAppFn func() (string, bool)
	stageFn     func(ctx context.Context, archivePath, stageDir, format, bundleName, expectedBundleID string) (string, error)
	launchFn    func(oldPID int, targetApp, stagedApp, stageDir, expectedBundleID string) error
	quitDelay   time.Duration
}

type releaseManifest struct {
	SchemaVersion int                      `json:"schema_version"`
	App           string                   `json:"app"`
	Version       string                   `json:"version"`
	PubDate       string                   `json:"pub_date,omitempty"`
	Channel       string                   `json:"channel,omitempty"`
	ReleaseURL    string                   `json:"release_url,omitempty"`
	Notes         string                   `json:"notes,omitempty"`
	Platforms     map[string]platformAsset `json:"platforms"`
}

type platformAsset struct {
	URL        string `json:"url"`
	Signature  string `json:"signature"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Format     string `json:"format,omitempty"`
	BundleName string `json:"bundle_name,omitempty"`
	BundleID   string `json:"bundle_id,omitempty"`
	MinOS      string `json:"min_os,omitempty"`
}

type selectedRelease struct {
	version     string
	platformKey string
	asset       platformAsset
	releaseURL  string
	notes       string
}

type stagedUpdate struct {
	version   string
	stageDir  string
	stagedApp string
}

// updaterManager coordinates periodic signed-manifest checks, Ed25519-verified
// background staging, one-shot handoff persistence, and detached self-updates.
type updaterManager struct {
	repo           string
	currentRaw     string
	currentVer     updaterSemver
	currentValid   bool
	manifestURL    string
	allowLocalhost bool
	pubKey         ed25519.PublicKey
	checkInterval  time.Duration
	initialDelay   time.Duration
	clientFactory  func(timeout time.Duration) *http.Client
	cacheDir       string
	handoffPath    string
	logger         *slog.Logger
	onQuit         func()
	quitDelay      time.Duration
	enableApply    bool
	targetApp      string
	selfUpdatable  bool
	stageFn        func(ctx context.Context, archivePath, stageDir, format, bundleName, expectedBundleID string) (string, error)
	launchFn       func(oldPID int, targetApp, stagedApp, stageDir, expectedBundleID string) error

	mu             sync.Mutex
	started        bool
	status         updateStatus
	etag           string
	lastCheck      time.Time
	selected       *selectedRelease
	staged         *stagedUpdate
	startupHandoff *updateHandoffPayload
}

// newUpdaterManager creates an updaterManager, consumes any pending startup
// handoff file once, and removes stale staging directories.
func newUpdaterManager(cfg updaterConfig) *updaterManager {
	repo := strings.TrimSpace(cfg.Repo)
	if repo == "" {
		repo = updaterDefaultRepo
	}

	manifestURL := strings.TrimSpace(cfg.ManifestURL)
	allowLocalhost := manifestURL != ""
	if envURL := strings.TrimSpace(os.Getenv("SUMI_UPDATE_MANIFEST_URL")); envURL != "" {
		manifestURL = envURL
		allowLocalhost = true
	}
	if manifestURL == "" {
		manifestURL = fmt.Sprintf("https://github.com/%s/releases/latest/download/latest.json", repo)
	}

	pubKeyB64 := strings.TrimSpace(cfg.PublicKeyB64)
	if envKey := strings.TrimSpace(os.Getenv("SUMI_UPDATE_TEST_PUBKEY")); envKey != "" {
		pubKeyB64 = envKey
	}
	if pubKeyB64 == "" {
		pubKeyB64 = updaterPubKeyB64
	}
	pubKey, err := decodeUpdaterPublicKey(pubKeyB64)
	if err != nil {
		// Fall back to the embedded key if an invalid env override is passed.
		pubKey, err = decodeUpdaterPublicKey(updaterPubKeyB64)
		if err != nil {
			panic(fmt.Sprintf("updater: embedded Ed25519 public key is invalid: %v", err))
		}
	}

	interval := cfg.CheckInterval
	if interval <= 0 {
		interval = updaterDefaultCheckInterval
	}
	delay := cfg.InitialDelay
	if delay < 0 {
		delay = 0
	}
	clientFactory := cfg.ClientFactory
	if clientFactory == nil {
		clientFactory = func(timeout time.Duration) *http.Client {
			return &http.Client{Timeout: timeout}
		}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	cacheDir := cfg.CacheDir
	if cacheDir == "" {
		if base, err := os.UserCacheDir(); err == nil {
			cacheDir = filepath.Join(base, "Sumi", "updates")
		} else {
			cacheDir = filepath.Join(os.TempDir(), "sumi-updates")
		}
	}
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = cacheDir
	}
	handoffPath := filepath.Join(dataDir, "update-handoff.json")

	rawVer := strings.TrimSpace(cfg.CurrentVersion)
	if override := strings.TrimSpace(os.Getenv("SUMI_UPDATE_TEST_VERSION")); override != "" {
		rawVer = override
	}
	ver, valid := parseUpdaterSemver(rawVer)
	displayVer := rawVer
	if valid {
		displayVer = ver.String()
	}

	detect := cfg.detectAppFn
	if detect == nil {
		detect = detectSelfAppBundle
	}
	stageFn := cfg.stageFn
	if stageFn == nil {
		stageFn = extractAndVerifyBundle
	}
	launchFn := cfg.launchFn
	if launchFn == nil {
		launchFn = spawnDetachedApplyHelper
	}
	quitDelay := cfg.quitDelay
	if quitDelay == 0 && cfg.launchFn == nil {
		// Let the HTTP commit response flush cleanly before tearing down the shell.
		quitDelay = 80 * time.Millisecond
	}

	var targetApp string
	var selfUpdatable bool
	if cfg.EnableApply {
		targetApp, selfUpdatable = detect()
	}

	// Consume any pending handoff file once on startup so a broken restore can
	// never cause a crash loop.
	startupHandoff := consumeHandoffFile(handoffPath)
	cleanupStaleStaging(cacheDir)

	return &updaterManager{
		repo:           repo,
		currentRaw:     rawVer,
		currentVer:     ver,
		currentValid:   valid,
		manifestURL:    manifestURL,
		allowLocalhost: allowLocalhost,
		pubKey:         pubKey,
		checkInterval:  interval,
		initialDelay:   delay,
		clientFactory:  clientFactory,
		cacheDir:       cacheDir,
		handoffPath:    handoffPath,
		logger:         logger,
		onQuit:         cfg.OnQuit,
		quitDelay:      quitDelay,
		enableApply:    cfg.EnableApply,
		targetApp:      targetApp,
		selfUpdatable:  selfUpdatable,
		stageFn:        stageFn,
		launchFn:       launchFn,
		startupHandoff: startupHandoff,
		status: updateStatus{
			Current: displayVer,
			State:   updateStateIdle,
		},
	}
}

// takeStartupHandoff returns and clears the startup handoff payload so the
// frontend consumes it strictly once.
func (m *updaterManager) takeStartupHandoff() *updateHandoffPayload {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.startupHandoff
	m.startupHandoff = nil
	return cloneUpdateHandoff(h)
}

func cloneUpdateHandoff(h *updateHandoffPayload) *updateHandoffPayload {
	if h == nil {
		return nil
	}
	cp := *h
	if h.AppState != nil {
		raw, err := json.Marshal(h.AppState)
		if err == nil {
			var stateCopy map[string]any
			if json.Unmarshal(raw, &stateCopy) == nil {
				cp.AppState = stateCopy
			}
		}
	}
	return &cp
}

// start launches the periodic background update checker. Dev builds (0.0.0-dev)
// skip automatic checks unless SUMI_UPDATE_TEST_VERSION is set.
func (m *updaterManager) start(ctx context.Context) {
	if !m.currentValid {
		return
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()

	go func() {
		if m.initialDelay > 0 {
			timer := time.NewTimer(m.initialDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		m.check(ctx, false)

		ticker := time.NewTicker(m.checkInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.check(ctx, false)
			}
		}
	}()
}

// currentStatus returns a copy of the current update state.
func (m *updaterManager) currentStatus() updateStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// checkNow queries the signed release manifest for the latest release.
func (m *updaterManager) checkNow(ctx context.Context) updateStatus {
	return m.check(ctx, false)
}

// checkManual runs a check unless a recent check already succeeded within the
// cooldown window.
func (m *updaterManager) checkManual(ctx context.Context) updateStatus {
	m.mu.Lock()
	recent := !m.lastCheck.IsZero() && time.Since(m.lastCheck) < updaterManualCheckCooldown
	st := m.status
	m.mu.Unlock()
	if recent {
		return st
	}
	return m.check(ctx, true)
}

func (m *updaterManager) check(ctx context.Context, manual bool) updateStatus {
	if !m.currentValid {
		return m.currentStatus()
	}

	m.mu.Lock()
	switch m.status.State {
	case updateStateChecking, updateStateDownloading, updateStateVerifying, updateStateReadyToRestart, updateStateInstalling:
		st := m.status
		m.mu.Unlock()
		return st
	}
	prevState := m.status.State
	etag := m.etag
	if manual {
		etag = ""
		m.status.Error = ""
	}
	m.status.State = updateStateChecking
	m.mu.Unlock()

	manifest, newETag, notModified, err := m.fetchVerifiedManifest(ctx, etag)

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status.State != updateStateChecking {
		return m.status
	}

	if err != nil {
		m.logger.Warn("Update check failed", "error", err.Error())
		m.status.State = prevState
		if manual {
			m.status.Error = err.Error()
		}
		return m.status
	}

	m.lastCheck = time.Now()
	if notModified {
		m.status.State = prevState
		return m.status
	}
	if newETag != "" {
		m.etag = newETag
	}

	latestVer, ok := parseUpdaterSemver(manifest.Version)
	if !ok {
		m.logger.Warn("Ignoring unparseable manifest version", "version", manifest.Version)
		m.status.State = prevState
		return m.status
	}

	releaseURL := strings.TrimSpace(manifest.ReleaseURL)
	if releaseURL == "" {
		releaseURL = fmt.Sprintf("https://github.com/%s/releases/tag/v%s", m.repo, latestVer.String())
	}

	if compareUpdaterSemver(latestVer, m.currentVer) <= 0 {
		m.selected = nil
		m.status.Latest = latestVer.String()
		m.status.Available = false
		m.status.CanApply = false
		m.status.ReleaseURL = releaseURL
		m.status.Notes = ""
		m.status.Error = ""
		m.status.State = updateStateIdle
		return m.status
	}

	platformKey, asset, found := resolveUpdaterPlatformAsset(manifest.Platforms)

	m.status.Latest = latestVer.String()
	m.status.Available = true
	m.status.ReleaseURL = releaseURL
	m.status.Notes = manifest.Notes
	m.status.Error = ""
	m.status.Progress = 0
	m.status.State = updateStateAvailable

	if found {
		m.selected = &selectedRelease{
			version:     latestVer.String(),
			platformKey: platformKey,
			asset:       asset,
			releaseURL:  releaseURL,
			notes:       manifest.Notes,
		}
		m.status.CanApply = m.selfUpdatable
	} else {
		m.selected = nil
		m.status.CanApply = false
	}

	m.logger.Info("Update available", "current", m.status.Current, "latest", latestVer.String(), "can_apply", m.status.CanApply)
	return m.status
}

// apply starts downloading, verifying (size, SHA-256, Ed25519), and staging the
// extracted `.app` bundle in the background. When staging succeeds, the manager
// transitions to ready_to_restart and waits for commitRestart.
func (m *updaterManager) apply() error {
	m.mu.Lock()
	switch m.status.State {
	case updateStateDownloading, updateStateVerifying, updateStateReadyToRestart, updateStateInstalling:
		m.mu.Unlock()
		return nil
	}
	if !m.status.Available {
		m.mu.Unlock()
		return errors.New("no update available")
	}
	if !m.status.CanApply || m.selected == nil {
		m.mu.Unlock()
		return errors.New("automatic install is unavailable for this installation")
	}
	sel := *m.selected

	m.status.State = updateStateDownloading
	m.status.Progress = 0
	m.status.Error = ""
	m.mu.Unlock()

	go m.runDownloadAndStage(sel)
	return nil
}

func (m *updaterManager) runDownloadAndStage(sel selectedRelease) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	fail := func(err error) {
		m.logger.Warn("Update staging failed", "version", sel.version, "error", err.Error())
		m.mu.Lock()
		m.status.State = updateStateError
		m.status.Progress = 0
		m.status.Error = err.Error()
		m.mu.Unlock()
	}

	if err := validateUpdaterDownloadURL(sel.asset.URL, m.repo, m.allowLocalhost); err != nil {
		fail(err)
		return
	}
	if sel.asset.Size <= 0 || sel.asset.Size > updaterMaxAssetBytes {
		fail(errors.New("release asset size out of allowed bounds"))
		return
	}

	stageDir := filepath.Join(m.cacheDir, sel.version)
	_ = os.RemoveAll(stageDir)
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		fail(fmt.Errorf("create staging dir: %w", err))
		return
	}

	archivePath := filepath.Join(stageDir, "update.archive")
	gotSHA256, gotSize, err := m.downloadFile(ctx, sel.asset.URL, archivePath, sel.asset.Size)
	if err != nil {
		_ = os.RemoveAll(stageDir)
		fail(fmt.Errorf("download archive: %w", err))
		return
	}

	m.mu.Lock()
	m.status.State = updateStateVerifying
	m.status.Progress = 100
	m.mu.Unlock()

	if gotSize != sel.asset.Size {
		_ = os.RemoveAll(stageDir)
		fail(fmt.Errorf("archive size mismatch (got %d, expected %d)", gotSize, sel.asset.Size))
		return
	}
	if !strings.EqualFold(gotSHA256, sel.asset.SHA256) {
		_ = os.RemoveAll(stageDir)
		fail(errors.New("archive SHA-256 checksum mismatch"))
		return
	}

	canonical := canonicalUpdaterAssetPayload(updaterAppSlug, sel.version, sel.platformKey, strings.ToLower(gotSHA256), gotSize)
	if err := verifyUpdaterEd25519B64(m.pubKey, []byte(canonical), sel.asset.Signature); err != nil {
		_ = os.RemoveAll(stageDir)
		fail(fmt.Errorf("Ed25519 signature verification failed: %w", err))
		return
	}

	bundleName := strings.TrimSpace(sel.asset.BundleName)
	if bundleName == "" {
		bundleName = updaterExpectedBundleName
	}
	bundleID := strings.TrimSpace(sel.asset.BundleID)
	if bundleID == "" {
		bundleID = updaterExpectedBundleID
	}
	if bundleID != updaterExpectedBundleID {
		_ = os.RemoveAll(stageDir)
		fail(fmt.Errorf("manifest bundle ID %q does not match %q", bundleID, updaterExpectedBundleID))
		return
	}

	stagedApp, err := m.stageFn(ctx, archivePath, stageDir, sel.asset.Format, bundleName, bundleID)
	if err != nil {
		_ = os.RemoveAll(stageDir)
		fail(err)
		return
	}

	m.mu.Lock()
	m.staged = &stagedUpdate{
		version:   sel.version,
		stageDir:  stageDir,
		stagedApp: stagedApp,
	}
	m.status.State = updateStateReadyToRestart
	m.status.Progress = 100
	m.status.Error = ""
	m.mu.Unlock()

	m.logger.Info("Update verified and staged", "version", sel.version)
}

// commitRestart writes update-handoff.json atomically (never logging appState),
// spawns the detached atomic-swap helper, and triggers graceful shell quit.
func (m *updaterManager) commitRestart(appState map[string]any) error {
	m.mu.Lock()
	if m.status.State == updateStateInstalling {
		m.mu.Unlock()
		return nil
	}
	if m.staged == nil {
		m.mu.Unlock()
		return errors.New("no verified staged update ready to install")
	}
	if m.targetApp == "" {
		m.mu.Unlock()
		return errors.New("target app bundle path unavailable")
	}
	staged := *m.staged
	targetApp := m.targetApp
	fromVersion := m.status.Current
	m.status.State = updateStateInstalling
	m.status.Progress = 100
	m.status.Error = ""
	m.mu.Unlock()

	if appState == nil {
		appState = map[string]any{}
	}
	handoff := updateHandoffPayload{
		SchemaVersion: 1,
		FromVersion:   fromVersion,
		ToVersion:     staged.version,
		CreatedAtMS:   time.Now().UnixMilli(),
		TTLMS:         updaterHandoffTTLMS,
		AppState:      appState,
	}
	if err := writeHandoffFile(m.handoffPath, handoff); err != nil {
		m.mu.Lock()
		m.status.State = updateStateReadyToRestart
		m.mu.Unlock()
		return err
	}

	if err := m.launchFn(os.Getpid(), targetApp, staged.stagedApp, staged.stageDir, updaterExpectedBundleID); err != nil {
		_ = os.Remove(m.handoffPath)
		m.mu.Lock()
		m.status.State = updateStateError
		m.status.Error = err.Error()
		m.mu.Unlock()
		return err
	}

	m.logger.Info("Update helper launched; quitting shell for restart", "to_version", staged.version)
	if m.onQuit != nil {
		if m.quitDelay > 0 {
			time.AfterFunc(m.quitDelay, m.onQuit)
		} else {
			go m.onQuit()
		}
	}
	return nil
}

func (m *updaterManager) httpClient(timeout time.Duration) *http.Client {
	base := m.clientFactory(timeout)
	if base == nil {
		base = &http.Client{Timeout: timeout}
	}
	// Shallow copy so redirect validation enforces the allowlist without mutating a shared client.
	c := *base
	prevRedirect := base.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if err := validateUpdaterDownloadURL(req.URL.String(), m.repo, m.allowLocalhost); err != nil {
			return err
		}
		if prevRedirect != nil {
			return prevRedirect(req, via)
		}
		return nil
	}
	return &c
}

func (m *updaterManager) fetchVerifiedManifest(ctx context.Context, etag string) (*releaseManifest, string, bool, error) {
	if err := validateUpdaterDownloadURL(m.manifestURL, m.repo, m.allowLocalhost); err != nil {
		return nil, "", false, err
	}

	client := m.httpClient(15 * time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.manifestURL, nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Sumi/"+m.currentRaw)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", false, fmt.Errorf("manifest fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("manifest returned %s", resp.Status)
	}

	newETag := resp.Header.Get("ETag")
	manifestBytes, err := io.ReadAll(io.LimitReader(resp.Body, updaterMaxManifestBytes+1))
	if err != nil {
		return nil, "", false, fmt.Errorf("read manifest body: %w", err)
	}
	if len(manifestBytes) > updaterMaxManifestBytes {
		return nil, "", false, errors.New("manifest exceeds maximum allowed size")
	}

	// Fetch companion Ed25519 signature (`latest.json.sig`) and verify exact manifest bytes.
	sigURL := m.manifestURL + ".sig"
	sigReq, err := http.NewRequestWithContext(ctx, http.MethodGet, sigURL, nil)
	if err != nil {
		return nil, "", false, err
	}
	sigReq.Header.Set("User-Agent", "Sumi/"+m.currentRaw)

	sigResp, err := client.Do(sigReq)
	if err != nil {
		return nil, "", false, fmt.Errorf("manifest signature fetch failed: %w", err)
	}
	defer sigResp.Body.Close()

	if sigResp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("manifest signature returned %s", sigResp.Status)
	}
	sigBytes, err := io.ReadAll(io.LimitReader(sigResp.Body, updaterMaxSigBytes))
	if err != nil {
		return nil, "", false, fmt.Errorf("read manifest signature: %w", err)
	}

	if err := verifyUpdaterEd25519B64(m.pubKey, manifestBytes, strings.TrimSpace(string(sigBytes))); err != nil {
		return nil, "", false, fmt.Errorf("manifest Ed25519 verification failed: %w", err)
	}

	var manifest releaseManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, "", false, fmt.Errorf("parse manifest JSON: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return nil, "", false, fmt.Errorf("unsupported manifest schema_version %d", manifest.SchemaVersion)
	}
	if manifest.App != updaterAppSlug {
		return nil, "", false, fmt.Errorf("manifest app mismatch: %q", manifest.App)
	}
	return &manifest, newETag, false, nil
}

func (m *updaterManager) downloadFile(ctx context.Context, rawURL, destPath string, expectedSize int64) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", "Sumi/"+m.currentRaw)

	resp, err := m.httpClient(5 * time.Minute).Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("download returned %s", resp.Status)
	}

	total := resp.ContentLength
	if total <= 0 {
		total = expectedSize
	}

	partPath := destPath + ".part"
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", 0, err
	}

	hasher := sha256.New()
	var written int64
	buf := make([]byte, 32*1024)
	lastPct := -1

	reader := io.LimitReader(resp.Body, updaterMaxAssetBytes+1)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			written += int64(n)
			if written > updaterMaxAssetBytes {
				f.Close()
				_ = os.Remove(partPath)
				return "", 0, errors.New("download exceeds maximum allowed size")
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				_ = os.Remove(partPath)
				return "", 0, werr
			}
			_, _ = hasher.Write(buf[:n])
			if total > 0 {
				pct := int(written * 100 / total)
				if pct > 99 {
					pct = 99
				}
				if pct != lastPct {
					lastPct = pct
					m.mu.Lock()
					if m.status.State == updateStateDownloading {
						m.status.Progress = pct
					}
					m.mu.Unlock()
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			_ = os.Remove(partPath)
			return "", 0, readErr
		}
	}

	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(partPath)
		return "", 0, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(partPath)
		return "", 0, err
	}
	if err := os.Rename(partPath, destPath); err != nil {
		_ = os.Remove(partPath)
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), written, nil
}

func canonicalUpdaterAssetPayload(app, version, platform, sha256Hex string, size int64) string {
	return fmt.Sprintf("v1:%s:%s:%s:%s:%d", app, version, platform, sha256Hex, size)
}

func decodeUpdaterPublicKey(pubkeyB64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubkeyB64))
	if err != nil {
		return nil, fmt.Errorf("decode public key base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

func verifyUpdaterEd25519B64(pubKey ed25519.PublicKey, message []byte, sigB64 string) error {
	sigBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil {
		return fmt.Errorf("decode signature base64: %w", err)
	}
	if len(sigBytes) != ed25519.SignatureSize {
		return fmt.Errorf("signature must be %d bytes, got %d", ed25519.SignatureSize, len(sigBytes))
	}
	if !ed25519.Verify(pubKey, message, sigBytes) {
		return errors.New("signature mismatch")
	}
	return nil
}

func validateUpdaterDownloadURL(rawURL, repo string, allowLocalhost bool) error {
	if allowLocalhost && (strings.HasPrefix(rawURL, "http://127.0.0.1:") || strings.HasPrefix(rawURL, "http://localhost:")) {
		return nil
	}
	if repo == "" {
		repo = updaterDefaultRepo
	}
	allowedPrefixes := []string{
		fmt.Sprintf("https://github.com/%s/", repo),
		"https://objects.githubusercontent.com/",
		"https://release-assets.githubusercontent.com/",
	}
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(rawURL, prefix) {
			return nil
		}
	}
	return fmt.Errorf("refusing untrusted update URL: %s", rawURL)
}

func resolveUpdaterPlatformAsset(platforms map[string]platformAsset) (string, platformAsset, bool) {
	var candidates []string
	switch goruntime.GOOS {
	case "darwin":
		switch goruntime.GOARCH {
		case "arm64":
			candidates = []string{"darwin-aarch64", "darwin-universal"}
		case "amd64":
			candidates = []string{"darwin-x86_64", "darwin-universal"}
		default:
			candidates = []string{"darwin-universal"}
		}
	case "windows":
		switch goruntime.GOARCH {
		case "arm64":
			candidates = []string{"windows-aarch64", "windows-x86_64"}
		default:
			candidates = []string{"windows-x86_64"}
		}
	}
	for _, key := range candidates {
		if asset, ok := platforms[key]; ok {
			return key, asset, true
		}
	}
	return "", platformAsset{}, false
}

func writeHandoffFile(path string, payload updateHandoffPayload) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create handoff dir: %w", err)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize handoff payload: %w", err)
	}
	tmpPath := path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open handoff temp file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write handoff temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("fsync handoff temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close handoff temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename handoff file: %w", err)
	}
	return nil
}

// consumeHandoffFile atomically consumes update-handoff.json once on startup.
// It renames and deletes the file before returning so a restore bug can never
// cause a crash loop.
func consumeHandoffFile(path string) *updateHandoffPayload {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil
	}
	consuming := path + ".consuming"
	if err := os.Rename(path, consuming); err != nil {
		_ = os.Remove(path)
		return nil
	}
	raw, err := os.ReadFile(consuming)
	_ = os.Remove(consuming)
	if err != nil {
		return nil
	}
	var parsed updateHandoffPayload
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	nowMS := time.Now().UnixMilli()
	ageMS := nowMS - parsed.CreatedAtMS
	if ageMS < 0 {
		ageMS = 0
	}
	if parsed.SchemaVersion != 1 || parsed.TTLMS <= 0 || ageMS > parsed.TTLMS {
		return nil
	}
	if parsed.AppState == nil {
		parsed.AppState = map[string]any{}
	}
	return &parsed
}

func cleanupStaleStaging(cacheDir string) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(cacheDir, entry.Name()))
		}
	}
}

type updaterSemver struct {
	major int
	minor int
	patch int
}

func (v updaterSemver) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

// parseUpdaterSemver extracts MAJOR.MINOR.PATCH from strings like "v6.0.8" or "6.0.8".
// Unstamped development builds ("0.0.0-dev", "dev", commit hashes) return false.
func parseUpdaterSemver(raw string) (updaterSemver, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "v")
	if s == "" || strings.EqualFold(s, "dev") || strings.EqualFold(s, "0.0.0-dev") {
		return updaterSemver{}, false
	}
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return updaterSemver{}, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return updaterSemver{}, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil || minor < 0 {
		return updaterSemver{}, false
	}
	patch := 0
	if len(parts) == 3 {
		patch, err = strconv.Atoi(parts[2])
		if err != nil || patch < 0 {
			return updaterSemver{}, false
		}
	}
	return updaterSemver{major: major, minor: minor, patch: patch}, true
}

func compareUpdaterSemver(a, b updaterSemver) int {
	if a.major != b.major {
		if a.major < b.major {
			return -1
		}
		return 1
	}
	if a.minor != b.minor {
		if a.minor < b.minor {
			return -1
		}
		return 1
	}
	if a.patch != b.patch {
		if a.patch < b.patch {
			return -1
		}
		return 1
	}
	return 0
}
