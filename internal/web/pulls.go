package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// GitHubAPI is GitHub's REST API.
const GitHubAPI = "https://api.github.com"

// pullsFresh is how long the pull requests fetched for one request are reused
// for the next, so several open pages share a fetch.
const pullsFresh = 15 * time.Second

// PullRequest is one of the repository's pull requests, as /pulls serves it.
type PullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	// State is open, merged, or closed (without merging).
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Branch string `json:"branch"`
	// Author is the GitHub account that opened it.
	Author string `json:"author"`
	// Agent is the swarm's agent that opened it, when known; nothing records
	// that yet, so it's always nil.
	Agent     *string `json:"agent"`
	UpdatedAt string  `json:"updated_at"`
}

// pulls fetches the repository's pull requests from GitHub, reusing the last
// fetch while it's fresh.
type pulls struct {
	swarm  Swarm
	client *http.Client

	mu      sync.Mutex
	fetched time.Time
	last    []PullRequest
}

func (p *pulls) get(ctx context.Context) ([]PullRequest, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last != nil && time.Since(p.fetched) < pullsFresh {
		return p.last, nil
	}
	got, err := p.fetch(ctx)
	if err != nil {
		return nil, err
	}
	p.last, p.fetched = got, time.Now()
	return got, nil
}

// fetch asks GitHub for the repository's most recently updated pull requests,
// open or not.
func (p *pulls) fetch(ctx context.Context) ([]PullRequest, error) {
	if p.swarm.Repo == "" {
		return nil, fmt.Errorf("the swarm's repository isn't known")
	}
	base := p.swarm.GitHub
	if base == "" {
		base = GitHubAPI
	}
	query := url.Values{"state": {"all"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"30"}}
	request, err := http.NewRequestWithContext(ctx, "GET", base+"/repos/"+p.swarm.Repo+"/pulls?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if p.swarm.Token != "" {
		request.Header.Set("Authorization", "Bearer "+p.swarm.Token)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", response.Status)
	}
	var found []struct {
		Number   int     `json:"number"`
		Title    string  `json:"title"`
		HTMLURL  string  `json:"html_url"`
		State    string  `json:"state"`
		Draft    bool    `json:"draft"`
		MergedAt *string `json:"merged_at"`
		Head     struct {
			Ref string `json:"ref"`
		} `json:"head"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.NewDecoder(response.Body).Decode(&found); err != nil {
		return nil, fmt.Errorf("GitHub's pull requests: %w", err)
	}
	result := make([]PullRequest, 0, len(found))
	for _, f := range found {
		state := f.State
		if f.MergedAt != nil {
			state = "merged"
		}
		result = append(result, PullRequest{
			Number: f.Number, Title: f.Title, URL: f.HTMLURL, State: state, Draft: f.Draft,
			Branch: f.Head.Ref, Author: f.User.Login, UpdatedAt: f.UpdatedAt,
		})
	}
	return result, nil
}
