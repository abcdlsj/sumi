//go:build !darwin

package main

import (
	"context"
	"errors"
)

func detectSelfAppBundle() (string, bool) {
	return "", false
}

func extractAndVerifyBundle(_ context.Context, _, _, _, _, _ string) (string, error) {
	return "", errors.New("automatic self-update is only supported for the macOS app bundle")
}

func spawnDetachedApplyHelper(_ int, _, _, _, _ string) error {
	return errors.New("automatic self-update is only supported for the macOS app bundle")
}
