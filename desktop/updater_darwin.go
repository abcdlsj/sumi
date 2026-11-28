//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// detectSelfAppBundle resolves the enclosing .app bundle when running from a
// packaged macOS application and checks that its parent directory is writable.
func detectSelfAppBundle() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	return inspectAppBundlePath(exe)
}

// inspectAppBundlePath is separated from os.Executable for unit testing.
func inspectAppBundlePath(exePath string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(exePath)
	if err != nil {
		resolved = exePath
	}
	// Expected layout: <dir>/<Name>.app/Contents/MacOS/<binary>
	macosDir := filepath.Dir(resolved)
	contentsDir := filepath.Dir(macosDir)
	appDir := filepath.Dir(contentsDir)
	if filepath.Base(macosDir) != "MacOS" || filepath.Base(contentsDir) != "Contents" || !strings.HasSuffix(appDir, ".app") {
		return "", false
	}
	if strings.HasPrefix(appDir, "/Volumes/") || strings.Contains(appDir, "AppTranslocation") {
		return "", false
	}
	parent := filepath.Dir(appDir)
	probe, err := os.CreateTemp(parent, ".sumi-update-probe-*")
	if err != nil {
		return "", false
	}
	probePath := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probePath)
	return appDir, true
}

// extractAndVerifyBundle unpacks the verified archive into stageDir/extracted
// and validates the bundle's executable, CFBundleIdentifier, and code signature.
func extractAndVerifyBundle(ctx context.Context, archivePath, stageDir, format, bundleName, expectedBundleID string) (string, error) {
	extractDir := filepath.Join(stageDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o700); err != nil {
		return "", fmt.Errorf("create extract dir: %w", err)
	}

	var cmd *exec.Cmd
	if format == "app-tar-gz" {
		cmd = exec.CommandContext(ctx, "/usr/bin/tar", "-xzf", archivePath, "-C", extractDir)
	} else {
		// ditto matches `ditto -c -k --sequesterRsrc --keepParent` in package-macos-app.sh
		// and preserves permissions, symlinks, and bundle metadata.
		cmd = exec.CommandContext(ctx, "/usr/bin/ditto", "-x", "-k", archivePath, extractDir)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("extract archive: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	stagedApp := filepath.Join(extractDir, bundleName)
	if st, err := os.Stat(stagedApp); err != nil || !st.IsDir() {
		return "", fmt.Errorf("extracted archive missing %s", bundleName)
	}

	plistPath := filepath.Join(stagedApp, "Contents", "Info.plist")
	out, err := exec.CommandContext(ctx, "/usr/libexec/PlistBuddy", "-c", "Print :CFBundleIdentifier", plistPath).Output()
	if err != nil {
		return "", fmt.Errorf("read staged Info.plist: %w", err)
	}
	actualID := strings.TrimSpace(string(out))
	if actualID != expectedBundleID {
		return "", fmt.Errorf("staged bundle ID mismatch (got %q, expected %q)", actualID, expectedBundleID)
	}

	exeName := updaterExpectedExeName
	if exeOut, err := exec.CommandContext(ctx, "/usr/libexec/PlistBuddy", "-c", "Print :CFBundleExecutable", plistPath).Output(); err == nil {
		if trimmed := strings.TrimSpace(string(exeOut)); trimmed != "" {
			exeName = trimmed
		}
	}
	binPath := filepath.Join(stagedApp, "Contents", "MacOS", exeName)
	info, err := os.Stat(binPath)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return "", errors.New("extracted bundle is missing executable Sumi binary")
	}

	// Verify the ad-hoc (or Developer ID) signature was not corrupted.
	if out, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", stagedApp).CombinedOutput(); err != nil {
		return "", fmt.Errorf("verify bundle signature: %v (%s)", err, strings.TrimSpace(string(out)))
	}

	return stagedApp, nil
}

const updaterApplyScript = `#!/bin/sh
set -eu

OLD_PID="$1"
TARGET_APP="$2"
STAGED_APP="$3"
STAGE_DIR="$4"
EXPECTED_BUNDLE_ID="$5"
LOG_FILE="$STAGE_DIR/apply.log"
exec >>"$LOG_FILE" 2>&1

echo "[updater] start pid=$OLD_PID target=$TARGET_APP"

KEYCHAIN="${HOME}/Library/Keychains/login.keychain-db"
PRESERVE_ID=""
CUR_AUTH=$(/usr/bin/codesign -dvv "$TARGET_APP" 2>&1 | /usr/bin/sed -n 's/^Authority=//p' | /usr/bin/head -n 1 || true)
NEW_SIG_INFO=$(/usr/bin/codesign -dvv "$STAGED_APP" 2>&1 || true)
if [ -n "$CUR_AUTH" ] && printf '%s\n' "$NEW_SIG_INFO" | /usr/bin/grep -q '^Signature=adhoc'; then
  if /usr/bin/security find-identity -v -p codesigning "$KEYCHAIN" 2>/dev/null | /usr/bin/grep -Fq "\"${CUR_AUTH}\""; then
    PRESERVE_ID="$CUR_AUTH"
  fi
fi

STAGED_ID=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$STAGED_APP/Contents/Info.plist" 2>/dev/null || true)
if [ "$STAGED_ID" != "$EXPECTED_BUNDLE_ID" ]; then
  echo "[updater] bundle id mismatch: got=$STAGED_ID want=$EXPECTED_BUNDLE_ID"
  exit 1
fi

# Wait for the running Sumi desktop shell to exit cleanly before swapping.
i=0
while /bin/kill -0 "$OLD_PID" 2>/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 150 ]; then
    echo "[updater] timed out waiting for old pid $OLD_PID"
    exit 1
  fi
  /bin/sleep 0.1
done

/usr/bin/xattr -dr com.apple.quarantine "$STAGED_APP" 2>/dev/null || true
if [ -n "$PRESERVE_ID" ]; then
  echo "[updater] preserving local signing identity: $PRESERVE_ID"
  if [ -d "$STAGED_APP/Contents/Frameworks" ]; then
    /usr/bin/find "$STAGED_APP/Contents/Frameworks" -maxdepth 1 -name '*.dylib' -print0 \
      | /usr/bin/xargs -0 -I{} /usr/bin/codesign --force --keychain "$KEYCHAIN" --sign "$PRESERVE_ID" "{}"
  fi
  /usr/bin/codesign --force --deep --keychain "$KEYCHAIN" --sign "$PRESERVE_ID" "$STAGED_APP"
fi
/usr/bin/codesign --verify --deep --strict "$STAGED_APP"

PARENT_DIR=$(/usr/bin/dirname "$TARGET_APP")
APP_NAME=$(/usr/bin/basename "$TARGET_APP")
NEW_TMP="$PARENT_DIR/.${APP_NAME}.new.$$"
OLD_TMP="$PARENT_DIR/.${APP_NAME}.old.$$"

# Stage onto the same directory/volume first so the swap is two atomic renames.
/usr/bin/ditto "$STAGED_APP" "$NEW_TMP"

if ! /bin/mv "$TARGET_APP" "$OLD_TMP"; then
  /bin/rm -rf "$NEW_TMP"
  exit 1
fi

if ! /bin/mv "$NEW_TMP" "$TARGET_APP"; then
  /bin/mv "$OLD_TMP" "$TARGET_APP" || true
  /bin/rm -rf "$NEW_TMP"
  exit 1
fi

/bin/rm -rf "$OLD_TMP" "$STAGE_DIR" || true

/usr/bin/open "$TARGET_APP"
`

// spawnDetachedApplyHelper writes and launches the detached apply.sh script
// that waits for oldPID to exit, swaps targetApp atomically, and relaunches it.
func spawnDetachedApplyHelper(oldPID int, targetApp, stagedApp, stageDir, expectedBundleID string) error {
	if targetApp == "" {
		return errors.New("target app bundle path is empty")
	}
	scriptPath := filepath.Join(stageDir, "apply.sh")
	if err := os.WriteFile(scriptPath, []byte(updaterApplyScript), 0o700); err != nil {
		return fmt.Errorf("write updater helper: %w", err)
	}

	cmd := exec.Command("/bin/sh", scriptPath, strconv.Itoa(oldPID), targetApp, stagedApp, stageDir, expectedBundleID)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start updater helper: %w", err)
	}
	_ = cmd.Process.Release()
	return nil
}
