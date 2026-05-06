package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const defaultSessionGap = 2 * time.Hour

type Config struct {
	Repos      []string    `toml:"repos"`
	SessionGap string      `toml:"session_gap"`
	Timezone   string      `toml:"timezone"`
	Noko       *NokoConfig `toml:"noko"`
	GCal       *GCalConfig `toml:"gcal"`
	LLM        *LLMConfig  `toml:"llm"`
}

type GCalConfig struct {
	Calendar string `toml:"calendar"`
}

type LLMConfig struct {
	APIKey  string `toml:"api_key"`
	Model   string `toml:"model"`
	BaseURL string `toml:"base_url"`
}

type NokoConfig struct {
	APIToken      string                  `toml:"api_token"`
	ProjectID     int                     `toml:"project_id"`
	ProjectName   string                  `toml:"project_name"`
	RepoProjects  map[string]RepoProject  `toml:"projects"`
	MinLogMinutes int                     `toml:"min_log_minutes"`
}

type RepoProject struct {
	ProjectID   int    `toml:"project_id"`
	ProjectName string `toml:"project_name"`
}

func (n *NokoConfig) ProjectForRepo(repo string) (int, string) {
	if n != nil && n.RepoProjects != nil {
		if rp, ok := n.RepoProjects[repo]; ok {
			return rp.ProjectID, rp.ProjectName
		}
	}
	if n != nil {
		return n.ProjectID, n.ProjectName
	}
	return 0, ""
}

type RuntimeConfig struct {
	Repos      []string
	SessionGap time.Duration
	Timezone   *time.Location
	Noko       *NokoConfig
	GCal       *GCalConfig
	LLM        *LLMConfig
	Path       string
}

func Load(configPathOverride string) (*RuntimeConfig, error) {
	path, err := resolvePath(configPathOverride)
	if err != nil {
		return nil, err
	}

	cfg := Config{}
	if path != "" {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read config file %q: %w", path, readErr)
		}
		if unmarshalErr := toml.Unmarshal(raw, &cfg); unmarshalErr != nil {
			return nil, fmt.Errorf("parse config file %q: %w", path, unmarshalErr)
		}
	}

	return buildRuntimeConfig(cfg, path)
}

func resolvePath(configPathOverride string) (string, error) {
	if configPathOverride != "" {
		return configPathOverride, nil
	}

	localPath := "wrklogr.toml"
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat %q: %w", localPath, err)
	}

	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}

	xdgPath := filepath.Join(userConfigDir, "wrklogr", "wrklogr.toml")
	if _, err := os.Stat(xdgPath); err == nil {
		return xdgPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat %q: %w", xdgPath, err)
	}

	return "", nil
}

func buildRuntimeConfig(cfg Config, path string) (*RuntimeConfig, error) {
	repos, err := normalizeAndValidateRepos(cfg.Repos)
	if err != nil {
		return nil, err
	}

	sessionGap := defaultSessionGap
	if cfg.SessionGap != "" {
		parsedGap, parseErr := time.ParseDuration(cfg.SessionGap)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid session_gap %q: %w", cfg.SessionGap, parseErr)
		}
		if parsedGap <= 0 {
			return nil, fmt.Errorf("session_gap must be > 0, got %q", cfg.SessionGap)
		}
		sessionGap = parsedGap
	}

	location := time.Local
	if cfg.Timezone != "" {
		loadedLoc, loadErr := time.LoadLocation(cfg.Timezone)
		if loadErr != nil {
			return nil, fmt.Errorf("invalid timezone %q: %w", cfg.Timezone, loadErr)
		}
		location = loadedLoc
	}

	return &RuntimeConfig{
		Repos:      repos,
		SessionGap: sessionGap,
		Timezone:   location,
		Noko:       cfg.Noko,
		GCal:       cfg.GCal,
		LLM:        cfg.LLM,
		Path:       path,
	}, nil
}

func normalizeAndValidateRepos(repos []string) ([]string, error) {
	normalized := make([]string, 0, len(repos))
	for _, repo := range repos {
		r := strings.TrimSpace(repo)
		if r == "" {
			continue
		}
		if err := validateRepo(r); err != nil {
			return nil, err
		}
		normalized = append(normalized, r)
	}
	return normalized, nil
}

func validateRepo(repo string) error {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("invalid repo %q: expected owner/repo", repo)
	}
	if strings.Contains(repo, " ") {
		return fmt.Errorf("invalid repo %q: must not contain spaces", repo)
	}
	return nil
}
