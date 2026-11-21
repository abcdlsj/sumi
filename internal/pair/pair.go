// Package pair bootstraps a local install: sign in, ensure a Space, pair this machine,
// and write the desktop shell configuration.
package pair

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

type Options struct {
	ServerURL   string
	Email       string
	Password    string
	DisplayName string
	SpaceSlug   string
	SpaceName   string
	ConfigPath  string
	DataDir     string
	Client      *http.Client // optional override for tests
}

type Result struct {
	SpaceSlug     string `json:"space_slug"`
	ComputerID    string `json:"computer_id"`
	ComputerName  string `json:"computer_name"`
	ConfigPath    string `json:"config_path"`
	CreatedUser   bool   `json:"created_user"`
	CreatedSpace  bool   `json:"created_space"`
	PairedMachine bool   `json:"paired_machine"`
}

type desktopConfig struct {
	ServerURL     string `json:"server_url"`
	ComputerToken string `json:"computer_token"`
	DataDir       string `json:"data_dir"`
}

func Run(opts Options) (Result, error) {
	if opts.ServerURL == "" {
		return Result{}, errors.New("server url is required")
	}
	if opts.Email == "" || opts.Password == "" {
		return Result{}, errors.New("email and password are required")
	}
	if opts.SpaceSlug == "" {
		opts.SpaceSlug = "studio"
	}
	if opts.SpaceName == "" {
		opts.SpaceName = capitalizeFirst(opts.SpaceSlug)
	}
	if opts.ConfigPath == "" {
		return Result{}, errors.New("config path is required")
	}

	client := opts.Client
	if client == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return Result{}, fmt.Errorf("create cookie jar: %w", err)
		}
		client = &http.Client{Jar: jar}
	}
	base := strings.TrimRight(opts.ServerURL, "/")

	res := Result{SpaceSlug: opts.SpaceSlug}

	// Sign in, registering when the account does not exist yet.
	loginErr := post(client, base+"/api/v1/auth/login", map[string]any{
		"email": opts.Email, "password": opts.Password,
	}, nil)
	if loginErr != nil {
		name := opts.DisplayName
		if name == "" {
			name = sanitizeDisplayName(opts.Email)
		}
		if err := post(client, base+"/api/v1/auth/register", map[string]any{
			"email": opts.Email, "password": opts.Password, "display_name": name,
		}, nil); err != nil {
			return Result{}, fmt.Errorf("sign in failed: %w", loginErr)
		}
		res.CreatedUser = true
	}

	var me struct {
		Spaces []struct {
			Slug string `json:"slug"`
		} `json:"spaces"`
	}
	if err := get(client, base+"/api/v1/auth/me", &me); err != nil {
		return Result{}, err
	}
	found := false
	for _, sp := range me.Spaces {
		if sp.Slug == opts.SpaceSlug {
			found = true
		}
	}
	if !found {
		if err := post(client, base+"/api/v1/spaces", map[string]any{
			"slug": opts.SpaceSlug, "name": opts.SpaceName,
		}, nil); err != nil {
			return Result{}, fmt.Errorf("create space %q: %w", opts.SpaceSlug, err)
		}
		res.CreatedSpace = true
	}

	hostname, _ := os.Hostname()
	name := hostname
	if name == "" {
		name = "this-computer"
	}
	var pairOut struct {
		Computer struct {
			ID string `json:"id"`
		} `json:"computer"`
		Token string `json:"token"`
	}
	if err := post(client, base+"/api/v1/computers/pair", map[string]any{
		"name": name, "hostname": hostname, "os": detectOS(),
	}, &pairOut); err != nil {
		return Result{}, fmt.Errorf("pair computer: %w", err)
	}
	if pairOut.Token == "" {
		return Result{}, errors.New("server returned an empty computer token")
	}
	res.PairedMachine = true
	res.ComputerID = pairOut.Computer.ID
	res.ComputerName = name
	res.ConfigPath = opts.ConfigPath

	cfg := desktopConfig{ServerURL: base, ComputerToken: pairOut.Token, DataDir: opts.DataDir}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("encode desktop config: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(opts.ConfigPath), 0o700); err != nil {
		return Result{}, fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(opts.ConfigPath, raw, 0o600); err != nil {
		return Result{}, fmt.Errorf("write desktop config: %w", err)
	}
	if err := os.Chmod(opts.ConfigPath, 0o600); err != nil {
		return Result{}, fmt.Errorf("restrict desktop config permissions: %w", err)
	}
	return res, nil
}

func capitalizeFirst(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func detectOS() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return "linux"
}

func sanitizeDisplayName(email string) string {
	name := email
	if at := strings.IndexByte(name, '@'); at > 0 {
		name = name[:at]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '.' || r == '-':
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" || strings.EqualFold(out, "all") {
		return "owner"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

func get(client *http.Client, url string, out any) error {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	return decode(resp, out)
}

func post(client *http.Client, url string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	return decode(resp, out)
}

func decode(resp *http.Response, out any) error {
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &envelope) == nil && envelope.Error.Code != "" {
			return fmt.Errorf("%s: %s", envelope.Error.Code, envelope.Error.Message)
		}
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
