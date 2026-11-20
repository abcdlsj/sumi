package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"time"
)

const pairPollInterval = 2 * time.Second

// pairingTicket holds the pairing secret, which becomes the computer token once confirmed.
type pairingTicket struct {
	ID        string
	Code      string
	Secret    string
	ExpiresAt time.Time
}

type pairingPoll struct {
	Status     string `json:"status"`
	ComputerID string `json:"computer_id,omitempty"`
}

var errPairingGone = errors.New("pairing not found")

type pairingClient struct {
	baseURL string
	http    *http.Client
}

func newPairingClient(baseURL string) *pairingClient {
	return &pairingClient{baseURL: baseURL, http: &http.Client{Timeout: 10 * time.Second}}
}

func (c *pairingClient) create(ctx context.Context, hostname, osName string) (pairingTicket, error) {
	body, _ := json.Marshal(map[string]string{"hostname": hostname, "os": osName, "daemon_version": ""})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/computer/pairings", bytes.NewReader(body))
	if err != nil {
		return pairingTicket{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Pairing struct {
			ID        string    `json:"id"`
			Code      string    `json:"code"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"pairing"`
		Secret string `json:"secret"`
	}
	if err := c.do(req, http.StatusCreated, &out); err != nil {
		return pairingTicket{}, fmt.Errorf("create pairing: %w", err)
	}
	if out.Pairing.ID == "" || out.Pairing.Code == "" || out.Secret == "" {
		return pairingTicket{}, errors.New("create pairing: incomplete response")
	}
	return pairingTicket{ID: out.Pairing.ID, Code: out.Pairing.Code, Secret: out.Secret, ExpiresAt: out.Pairing.ExpiresAt}, nil
}

func (c *pairingClient) poll(ctx context.Context, t pairingTicket) (pairingPoll, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/computer/pairings/"+url.PathEscape(t.ID), nil)
	if err != nil {
		return pairingPoll{}, err
	}
	req.Header.Set("Authorization", "Bearer "+t.Secret)
	var out pairingPoll
	if err := c.do(req, http.StatusOK, &out); err != nil {
		return pairingPoll{}, fmt.Errorf("poll pairing: %w", err)
	}
	return out, nil
}

// do never surfaces response bodies so secrets and codes cannot leak into logs.
func (c *pairingClient) do(req *http.Request, want int, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errPairingGone
	}
	if resp.StatusCode != want {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(out)
}

func pairURL(baseURL string, t pairingTicket) string {
	q := url.Values{}
	q.Set("pair", t.ID)
	q.Set("code", t.Code)
	q.Set("shell", "desktop")
	return baseURL + "/?" + q.Encode()
}

func detectOS() string {
	if goruntime.GOOS == "darwin" {
		return "macos"
	}
	return "linux"
}

// pairer drives the confirm-based pairing loop until confirmation or ctx cancellation.
type pairer struct {
	client   *pairingClient
	hostname string
	osName   string
	interval time.Duration
	logger   *slog.Logger
	// onURL is called with each new pairing URL while still unpaired.
	onURL func(string)
}

// run returns the confirmed secret, or ctx.Err() when stopped. initial may be nil.
func (p *pairer) run(ctx context.Context, initial *pairingTicket) (string, error) {
	var current *pairingTicket
	if initial != nil {
		t := *initial
		current = &t
	}
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		if current == nil {
			t, err := p.client.create(ctx, p.hostname, p.osName)
			if err != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				p.logger.Warn("Cannot create pairing", "error", err.Error())
			} else {
				current = &t
				p.onURL(pairURL(p.client.baseURL, t))
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
		if current == nil {
			continue
		}
		res, err := p.client.poll(ctx, *current)
		switch {
		case ctx.Err() != nil:
			return "", ctx.Err()
		case errors.Is(err, errPairingGone):
			current = nil
		case err != nil:
			p.logger.Warn("Cannot poll pairing", "pairing_id", current.ID, "error", err.Error())
		case res.Status == "confirmed":
			p.logger.Info("Computer pairing confirmed", "pairing_id", current.ID, "computer_id", res.ComputerID)
			return current.Secret, nil
		case res.Status == "expired":
			current = nil
		}
	}
}

// writeConfigAtomic writes cfg with 0600 permissions via temp file + rename so a crash never leaves a partial token.
func writeConfigAtomic(path string, cfg Config) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".desktop-*.json")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func newPairer(baseURL string, logger *slog.Logger) *pairer {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "sumi-desktop"
	}
	return &pairer{
		client:   newPairingClient(baseURL),
		hostname: host,
		osName:   detectOS(),
		interval: pairPollInterval,
		logger:   logger,
		onURL:    func(string) {},
	}
}
