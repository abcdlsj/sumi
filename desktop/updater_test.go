package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testUpdaterKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	return pub, priv, base64.StdEncoding.EncodeToString(pub)
}

func signUpdaterBytesB64(priv ed25519.PrivateKey, msg []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))
}

func makeSignedUpdaterManifest(t *testing.T, priv ed25519.PrivateKey, m releaseManifest) ([]byte, string) {
	t.Helper()
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	raw = append(raw, '\n')
	return raw, signUpdaterBytesB64(priv, raw)
}

func testPlatformKey() string {
	switch goruntime.GOOS {
	case "darwin":
		if goruntime.GOARCH == "arm64" {
			return "darwin-aarch64"
		}
		return "darwin-x86_64"
	case "windows":
		if goruntime.GOARCH == "arm64" {
			return "windows-aarch64"
		}
		return "windows-x86_64"
	default:
		return "darwin-universal"
	}
}

func TestUpdaterEmbeddedPublicKeyValid(t *testing.T) {
	pub, err := decodeUpdaterPublicKey(updaterPubKeyB64)
	if err != nil {
		t.Fatalf("embedded public key failed to decode: %v", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("expected %d-byte pubkey, got %d", ed25519.PublicKeySize, len(pub))
	}
}

func TestUpdaterParseAndCompareSemver(t *testing.T) {
	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"v6.0.8", "6.0.8", true},
		{"6.0.8", "6.0.8", true},
		{"v6.0.8-rc.1", "6.0.8", true},
		{"v1.2", "1.2.0", true},
		{"0.0.0-dev", "", false},
		{"v0.0.0-dev", "", false},
		{"dev", "", false},
		{"abc1234", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		v, ok := parseUpdaterSemver(tc.raw)
		if ok != tc.ok {
			t.Errorf("parseUpdaterSemver(%q) ok = %v, want %v", tc.raw, ok, tc.ok)
			continue
		}
		if ok && v.String() != tc.want {
			t.Errorf("parseUpdaterSemver(%q) = %s, want %s", tc.raw, v.String(), tc.want)
		}
	}

	a, _ := parseUpdaterSemver("v6.0.7")
	b, _ := parseUpdaterSemver("v6.0.8")
	c, _ := parseUpdaterSemver("v6.1.0")
	d, _ := parseUpdaterSemver("v7.0.0")
	if compareUpdaterSemver(a, b) >= 0 {
		t.Errorf("expected %s < %s", a, b)
	}
	if compareUpdaterSemver(b, c) >= 0 {
		t.Errorf("expected %s < %s", b, c)
	}
	if compareUpdaterSemver(c, d) >= 0 {
		t.Errorf("expected %s < %s", c, d)
	}
	if compareUpdaterSemver(a, a) != 0 {
		t.Errorf("expected %s == %s", a, a)
	}
}

func TestUpdaterEd25519VerifyValidAndTampered(t *testing.T) {
	pub, priv, _ := testUpdaterKeyPair(t)
	payload := canonicalUpdaterAssetPayload(updaterAppSlug, "6.0.9", "darwin-aarch64", "abc123", 42)
	sig := signUpdaterBytesB64(priv, []byte(payload))

	if err := verifyUpdaterEd25519B64(pub, []byte(payload), sig); err != nil {
		t.Fatalf("expected valid signature to verify: %v", err)
	}
	if err := verifyUpdaterEd25519B64(pub, []byte(payload+"x"), sig); err == nil {
		t.Fatal("expected tampered payload to be rejected")
	}
	if err := verifyUpdaterEd25519B64(pub, []byte(payload), "not-valid-base64!!!"); err == nil {
		t.Fatal("expected invalid base64 signature to be rejected")
	}
}

func TestUpdaterValidateDownloadURL(t *testing.T) {
	allowed := []string{
		"https://github.com/abcdlsj/sumi/releases/latest/download/latest.json",
		"https://objects.githubusercontent.com/github-production-release-asset/foo.zip",
		"https://release-assets.githubusercontent.com/github-production-release-asset/foo.zip",
	}
	for _, u := range allowed {
		if err := validateUpdaterDownloadURL(u, updaterDefaultRepo, false); err != nil {
			t.Errorf("expected allowed URL %q, got error: %v", u, err)
		}
	}

	blocked := []string{
		"https://evil.example.com/latest.json",
		"https://github.com/other/repo/releases/latest/download/latest.json",
		"http://127.0.0.1:8080/latest.json",
		"http://localhost:8080/latest.json",
	}
	for _, u := range blocked {
		if err := validateUpdaterDownloadURL(u, updaterDefaultRepo, false); err == nil {
			t.Errorf("expected blocked URL %q to fail", u)
		}
	}

	if err := validateUpdaterDownloadURL("http://127.0.0.1:8080/latest.json", updaterDefaultRepo, true); err != nil {
		t.Errorf("expected localhost allowed when allowLocalhost=true: %v", err)
	}
}

func TestUpdaterCheckSignedManifestAndETag(t *testing.T) {
	_, priv, pubB64 := testUpdaterKeyPair(t)
	plat := testPlatformKey()

	manifestBytes, manifestSig := makeSignedUpdaterManifest(t, priv, releaseManifest{
		SchemaVersion: 1,
		App:           updaterAppSlug,
		Version:       "6.0.9",
		ReleaseURL:    "https://github.com/abcdlsj/sumi/releases/tag/v6.0.9",
		Notes:         "Seamless updater",
		Platforms: map[string]platformAsset{
			plat: {
				URL:        "https://github.com/abcdlsj/sumi/releases/download/v6.0.9/Sumi-6.0.9-aarch64-apple-darwin.app.zip",
				Signature:  "placeholder",
				SHA256:     strings.Repeat("a", 64),
				Size:       1024,
				Format:     "app-zip",
				BundleName: updaterExpectedBundleName,
				BundleID:   updaterExpectedBundleID,
			},
		},
	})

	var manifestHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			manifestHits.Add(1)
			if r.Header.Get("If-None-Match") == `"etag-v609"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"etag-v609"`)
			_, _ = w.Write(manifestBytes)
		case "/latest.json.sig":
			_, _ = w.Write([]byte(manifestSig + "\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("SUMI_UPDATE_MANIFEST_URL", srv.URL+"/latest.json")
	t.Setenv("SUMI_UPDATE_TEST_VERSION", "6.0.8")
	t.Setenv("SUMI_UPDATE_TEST_PUBKEY", pubB64)

	m := newUpdaterManager(updaterConfig{
		CurrentVersion: "0.0.0-dev",
		DataDir:        t.TempDir(),
		CacheDir:       t.TempDir(),
		EnableApply:    true,
		detectAppFn:    func() (string, bool) { return "/Applications/Sumi.app", true },
	})

	st := m.checkNow(context.Background())
	if !st.Available || st.Latest != "6.0.9" || st.State != updateStateAvailable || st.Notes != "Seamless updater" {
		t.Fatalf("unexpected status after first check: %+v", st)
	}
	if goruntime.GOOS == "darwin" && !st.CanApply {
		t.Fatalf("expected can_apply=true on darwin, got %+v", st)
	}

	// Second check should hit 304 Not Modified and preserve available state.
	st2 := m.checkNow(context.Background())
	if !st2.Available || st2.Latest != "6.0.9" || st2.State != updateStateAvailable {
		t.Fatalf("unexpected status after 304 check: %+v", st2)
	}
	if manifestHits.Load() != 2 {
		t.Fatalf("expected 2 manifest requests, got %d", manifestHits.Load())
	}
}

func TestUpdaterCheckRejectsTamperedManifestAndMismatchedApp(t *testing.T) {
	_, priv, pubB64 := testUpdaterKeyPair(t)

	goodBytes, goodSig := makeSignedUpdaterManifest(t, priv, releaseManifest{
		SchemaVersion: 1,
		App:           updaterAppSlug,
		Version:       "6.0.9",
		Platforms:     map[string]platformAsset{},
	})
	tamperedBytes := append([]byte{}, goodBytes...)
	tamperedBytes = append(tamperedBytes, ' ')

	wrongAppBytes, wrongAppSig := makeSignedUpdaterManifest(t, priv, releaseManifest{
		SchemaVersion: 1,
		App:           "other-app",
		Version:       "6.0.9",
		Platforms:     map[string]platformAsset{},
	})

	cases := []struct {
		name string
		body []byte
		sig  string
	}{
		{"tampered manifest bytes", tamperedBytes, goodSig},
		{"wrong app slug", wrongAppBytes, wrongAppSig},
		{"bad signature", goodBytes, base64.StdEncoding.EncodeToString(make([]byte, 64))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/latest.json":
					_, _ = w.Write(tc.body)
				case "/latest.json.sig":
					_, _ = w.Write([]byte(tc.sig))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			m := newUpdaterManager(updaterConfig{
				CurrentVersion: "v6.0.8",
				ManifestURL:    srv.URL + "/latest.json",
				PublicKeyB64:   pubB64,
				DataDir:        t.TempDir(),
				CacheDir:       t.TempDir(),
			})

			st := m.checkManual(context.Background())
			if st.Available || st.State != updateStateIdle || st.Error == "" {
				t.Fatalf("expected rejected manifest to remain idle with error, got %+v", st)
			}
		})
	}
}

func TestUpdaterCheckUpToDate(t *testing.T) {
	_, priv, pubB64 := testUpdaterKeyPair(t)
	manifestBytes, manifestSig := makeSignedUpdaterManifest(t, priv, releaseManifest{
		SchemaVersion: 1,
		App:           updaterAppSlug,
		Version:       "6.0.8",
		ReleaseURL:    "https://github.com/abcdlsj/sumi/releases/tag/v6.0.8",
		Platforms:     map[string]platformAsset{},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			_, _ = w.Write(manifestBytes)
		case "/latest.json.sig":
			_, _ = w.Write([]byte(manifestSig))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	m := newUpdaterManager(updaterConfig{
		CurrentVersion: "v6.0.8",
		ManifestURL:    srv.URL + "/latest.json",
		PublicKeyB64:   pubB64,
		DataDir:        t.TempDir(),
		CacheDir:       t.TempDir(),
	})
	st := m.checkNow(context.Background())
	if st.Available || st.State != updateStateIdle {
		t.Fatalf("expected idle/not available, got %+v", st)
	}
}

func TestUpdaterApplyTwoPhaseReadyToRestartAndCommitRestart(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		t.Skip("platform asset resolution targets darwin/windows desktop shells")
	}
	_, priv, pubB64 := testUpdaterKeyPair(t)
	plat := testPlatformKey()

	zipBytes := []byte("fake-signed-zip-archive-payload-for-sumi-test")
	sum := sha256.Sum256(zipBytes)
	hexSum := hex.EncodeToString(sum[:])
	assetSig := signUpdaterBytesB64(priv, []byte(canonicalUpdaterAssetPayload(updaterAppSlug, "6.0.9", plat, hexSum, int64(len(zipBytes)))))

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifestBytes, manifestSig := makeSignedUpdaterManifest(t, priv, releaseManifest{
			SchemaVersion: 1,
			App:           updaterAppSlug,
			Version:       "6.0.9",
			ReleaseURL:    "https://github.com/abcdlsj/sumi/releases/tag/v6.0.9",
			Platforms: map[string]platformAsset{
				plat: {
					URL:        srvURL + "/dl/Sumi-6.0.9.app.zip",
					Signature:  assetSig,
					SHA256:     hexSum,
					Size:       int64(len(zipBytes)),
					Format:     "app-zip",
					BundleName: updaterExpectedBundleName,
					BundleID:   updaterExpectedBundleID,
				},
			},
		})
		switch r.URL.Path {
		case "/latest.json":
			_, _ = w.Write(manifestBytes)
		case "/latest.json.sig":
			_, _ = w.Write([]byte(manifestSig))
		case "/dl/Sumi-6.0.9.app.zip":
			_, _ = w.Write(zipBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	srvURL = srv.URL
	defer srv.Close()

	dataDir := t.TempDir()
	cacheDir := t.TempDir()
	var launchCalls atomic.Int32
	var launchedTarget, launchedBundleID string
	quitCh := make(chan struct{}, 1)

	m := newUpdaterManager(updaterConfig{
		CurrentVersion: "v6.0.8",
		ManifestURL:    srv.URL + "/latest.json",
		PublicKeyB64:   pubB64,
		DataDir:        dataDir,
		CacheDir:       cacheDir,
		EnableApply:    true,
		OnQuit:         func() { quitCh <- struct{}{} },
		detectAppFn:    func() (string, bool) { return "/Applications/Sumi.app", true },
		stageFn: func(_ context.Context, archivePath, stageDir, _, bundleName, bundleID string) (string, error) {
			data, err := os.ReadFile(archivePath)
			if err != nil {
				return "", err
			}
			if string(data) != string(zipBytes) {
				return "", fmt.Errorf("downloaded archive mismatch")
			}
			if bundleID != updaterExpectedBundleID {
				return "", fmt.Errorf("unexpected bundleID %q", bundleID)
			}
			staged := filepath.Join(stageDir, "extracted", bundleName)
			if err := os.MkdirAll(staged, 0o700); err != nil {
				return "", err
			}
			return staged, nil
		},
		launchFn: func(_ int, targetApp, _, _, expectedBundleID string) error {
			launchedTarget = targetApp
			launchedBundleID = expectedBundleID
			launchCalls.Add(1)
			return nil
		},
	})

	st := m.checkNow(context.Background())
	if !st.Available || !st.CanApply {
		t.Fatalf("expected available & can_apply, got %+v", st)
	}
	if err := m.apply(); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	// Phase 1: wait for ready_to_restart (must NOT invoke launchFn or OnQuit yet).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st = m.currentStatus()
		if st.State == updateStateReadyToRestart || st.State == updateStateError {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if st.State != updateStateReadyToRestart || st.Progress != 100 {
		t.Fatalf("expected ready_to_restart at 100%%, got %+v", st)
	}
	if launchCalls.Load() != 0 {
		t.Fatalf("launchFn should not run before commitRestart")
	}

	// Phase 2: commitRestart writes handoff, spawns helper, and fires OnQuit.
	err := m.commitRestart(map[string]any{
		"space_slug":     "studio",
		"scroll_conv_id": "conv-1",
		"scroll_top":     240,
	})
	if err != nil {
		t.Fatalf("commitRestart failed: %v", err)
	}
	if launchCalls.Load() != 1 || launchedTarget != "/Applications/Sumi.app" || launchedBundleID != updaterExpectedBundleID {
		t.Fatalf("unexpected launch call: count=%d target=%q id=%q", launchCalls.Load(), launchedTarget, launchedBundleID)
	}
	if st := m.currentStatus(); st.State != updateStateInstalling {
		t.Fatalf("expected installing state, got %+v", st)
	}
	select {
	case <-quitCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OnQuit")
	}

	// Simulate next launch consuming the persisted handoff file once.
	mNext := newUpdaterManager(updaterConfig{
		CurrentVersion: "v6.0.9",
		DataDir:        dataDir,
		CacheDir:       cacheDir,
	})
	if _, err := os.Stat(filepath.Join(dataDir, "update-handoff.json")); !os.IsNotExist(err) {
		t.Fatalf("expected update-handoff.json to be deleted after startup consume")
	}
	taken := mNext.takeStartupHandoff()
	if taken == nil || taken.FromVersion != "6.0.8" || taken.ToVersion != "6.0.9" || taken.AppState["space_slug"] != "studio" {
		t.Fatalf("unexpected taken handoff: %+v", taken)
	}
	if again := mNext.takeStartupHandoff(); again != nil {
		t.Fatalf("expected second takeStartupHandoff to return nil, got %+v", again)
	}
}

func TestUpdaterApplyRejectsTamperedChecksumSizeAndSignature(t *testing.T) {
	if goruntime.GOOS != "darwin" && goruntime.GOOS != "windows" {
		t.Skip("platform asset resolution targets darwin/windows desktop shells")
	}
	_, priv, pubB64 := testUpdaterKeyPair(t)
	plat := testPlatformKey()
	zipBytes := []byte("genuine-archive-content")
	sum := sha256.Sum256(zipBytes)
	hexSum := hex.EncodeToString(sum[:])
	goodSig := signUpdaterBytesB64(priv, []byte(canonicalUpdaterAssetPayload(updaterAppSlug, "6.0.9", plat, hexSum, int64(len(zipBytes)))))
	badSig := signUpdaterBytesB64(priv, []byte(canonicalUpdaterAssetPayload(updaterAppSlug, "6.0.9", plat, hexSum, int64(len(zipBytes))+1)))

	cases := []struct {
		name     string
		sha256   string
		size     int64
		sig      string
		download []byte
	}{
		{"sha256 mismatch", strings.Repeat("0", 64), int64(len(zipBytes)), goodSig, zipBytes},
		{"size mismatch", hexSum, int64(len(zipBytes)) + 10, goodSig, zipBytes},
		{"asset signature mismatch", hexSum, int64(len(zipBytes)), badSig, zipBytes},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var srvURL string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				manifestBytes, manifestSig := makeSignedUpdaterManifest(t, priv, releaseManifest{
					SchemaVersion: 1,
					App:           updaterAppSlug,
					Version:       "6.0.9",
					Platforms: map[string]platformAsset{
						plat: {
							URL:        srvURL + "/dl/zip",
							Signature:  tc.sig,
							SHA256:     tc.sha256,
							Size:       tc.size,
							Format:     "app-zip",
							BundleName: updaterExpectedBundleName,
							BundleID:   updaterExpectedBundleID,
						},
					},
				})
				switch r.URL.Path {
				case "/latest.json":
					_, _ = w.Write(manifestBytes)
				case "/latest.json.sig":
					_, _ = w.Write([]byte(manifestSig))
				case "/dl/zip":
					_, _ = w.Write(tc.download)
				default:
					http.NotFound(w, r)
				}
			}))
			srvURL = srv.URL
			defer srv.Close()

			cacheDir := t.TempDir()
			m := newUpdaterManager(updaterConfig{
				CurrentVersion: "v6.0.8",
				ManifestURL:    srv.URL + "/latest.json",
				PublicKeyB64:   pubB64,
				DataDir:        t.TempDir(),
				CacheDir:       cacheDir,
				EnableApply:    true,
				detectAppFn:    func() (string, bool) { return "/Applications/Sumi.app", true },
			})

			m.checkNow(context.Background())
			if err := m.apply(); err != nil {
				t.Fatalf("apply failed: %v", err)
			}

			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				st := m.currentStatus()
				if st.State == updateStateError {
					if st.Error == "" {
						t.Fatal("expected non-empty error message")
					}
					if _, err := os.Stat(filepath.Join(cacheDir, "6.0.9")); !os.IsNotExist(err) {
						t.Fatal("expected staging dir to be cleaned up on failure")
					}
					return
				}
				time.Sleep(15 * time.Millisecond)
			}
			t.Fatalf("timed out waiting for error state; status=%+v", m.currentStatus())
		})
	}
}

func TestUpdaterHandoffOneShotAndTTL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-handoff.json")

	fresh := updateHandoffPayload{
		SchemaVersion: 1,
		FromVersion:   "6.0.8",
		ToVersion:     "6.0.9",
		CreatedAtMS:   time.Now().UnixMilli(),
		TTLMS:         updaterHandoffTTLMS,
		AppState:      map[string]any{"space_slug": "studio"},
	}
	if err := writeHandoffFile(path, fresh); err != nil {
		t.Fatalf("writeHandoffFile: %v", err)
	}
	got := consumeHandoffFile(path)
	if got == nil || got.ToVersion != "6.0.9" {
		t.Fatalf("expected valid handoff, got %+v", got)
	}
	if again := consumeHandoffFile(path); again != nil {
		t.Fatalf("expected one-shot handoff consumption, got %+v", again)
	}

	// Expired handoff is discarded and deleted.
	expired := fresh
	expired.CreatedAtMS = time.Now().Add(-3 * time.Minute).UnixMilli()
	expired.TTLMS = 60_000
	if err := writeHandoffFile(path, expired); err != nil {
		t.Fatalf("write expired handoff: %v", err)
	}
	if got := consumeHandoffFile(path); got != nil {
		t.Fatalf("expected expired handoff to be discarded, got %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected expired handoff file to be removed")
	}
}
