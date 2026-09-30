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

// skillAttempts bounds how long a start waits for the app serving its
// skills: an agent and its app often start together.
const (
	skillAttempts = 10
	skillRetry    = 3 * time.Second
)

// skillsFromEnv loads the skills the app declared for this agent, from
// CORTEX_SKILLS_FILE or the app's CORTEX_SKILLS_URL. Neither set, the agent
// has no declared skill.
func skillsFromEnv(ctx context.Context) ([]host.Skill, error) {
	if path := strings.TrimSpace(os.Getenv("CORTEX_SKILLS_FILE")); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("CORTEX_SKILLS_FILE: %w", err)
		}
		return decodeSkills(raw)
	}
	url := strings.TrimSpace(os.Getenv("CORTEX_SKILLS_URL"))
	if url == "" {
		return nil, nil
	}
	var last error
	for attempt := 1; attempt <= skillAttempts; attempt++ {
		skills, err := fetchSkills(ctx, url)
		if err == nil {
			return skills, nil
		}
		last = err
		slog.Warn("cortex: skills not loaded yet", "url", url, "attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(skillRetry):
		}
	}
	return nil, fmt.Errorf("CORTEX_SKILLS_URL %s: %w", url, last)
}

func fetchSkills(ctx context.Context, url string) ([]host.Skill, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("answered %d", res.StatusCode)
	}
	var c host.Catalog
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil {
		return nil, err
	}
	return c.Skills, nil
}

func decodeSkills(raw []byte) ([]host.Skill, error) {
	var c host.Catalog
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return c.Skills, nil
}
