package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lalternative/packages/go/cortex/host"
)

// The first retries are quick, an agent and its app often start together;
// after that an app that stays down is asked every skillRetryMax.
const (
	skillRetryMin = 3 * time.Second
	skillRetryMax = 30 * time.Second
)

// skillsFromFile reads CORTEX_SKILLS_FILE, a file shipped with the agent.
func skillsFromFile() (host.Catalog, error) {
	path := strings.TrimSpace(os.Getenv("CORTEX_SKILLS_FILE"))
	if path == "" {
		return host.Catalog{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return host.Catalog{}, fmt.Errorf("CORTEX_SKILLS_FILE: %w", err)
	}
	var c host.Catalog
	if err := json.Unmarshal(raw, &c); err != nil {
		return host.Catalog{}, fmt.Errorf("CORTEX_SKILLS_FILE: %w", err)
	}
	return c, nil
}

// loadSkillsFromURL fetches the app's skills.json (CORTEX_SKILLS_URL) while
// the agent already serves, retrying until the app answers, then hands them
// to set. It returns when they are set or ctx ends.
func loadSkillsFromURL(ctx context.Context, url string, set func(host.Catalog) error) {
	wait := skillRetryMin
	for attempt := 1; ; attempt++ {
		c, err := fetchSkills(ctx, url)
		if err == nil {
			err = set(c)
		}
		if err == nil {
			slog.Info("cortex: skills loaded", "url", url, "skills", len(c.Skills), "app_instructions", c.Instructions != "", "attempt", attempt)
			return
		}
		slog.Warn("cortex: skills not loaded yet", "url", url, "attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < skillRetryMax {
			wait = min(wait*2, skillRetryMax)
		}
	}
}

func fetchSkills(ctx context.Context, url string) (host.Catalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return host.Catalog{}, err
	}
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return host.Catalog{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return host.Catalog{}, fmt.Errorf("answered %d", res.StatusCode)
	}
	var c host.Catalog
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil {
		return host.Catalog{}, err
	}
	return c, nil
}
