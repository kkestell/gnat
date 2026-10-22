package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/state"
)

// GitHubAPI is GitHub's REST API.
const GitHubAPI = "https://api.github.com"

// githubTimeout bounds a GitHub operation, and maxDiffBytes how much of a diff is sent.
const (
	githubTimeout = 60 * time.Second
	maxDiffBytes  = 4 << 20
	// maxListed is the most pull requests, and maxItems the most comments of each kind,
	// one request returns.
	maxListed = 100
	maxItems  = 100
)

var repoRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\z`)

// GitHub is how the control plane acts on GitHub for the agents. It is the only thing in
// the swarm that holds the token, and so the only place the rules can be enforced: an
// agent can talk to the control plane without the gnat-container commands, so nothing
// the request says about who is asking, or where, is believed. The agent is whoever's
// secret it carries, the repository is always Repo, and the head of a pull request must
// be under that agent's own branch prefix (see state.BranchPrefix).
type GitHub struct {
	// Swarm is the swarm's name, the first part of every agent's branch prefix.
	Swarm string
	// Repo is the swarm's repository, OWNER/NAME.
	Repo  string
	Token string
	// API is GitHub's API, GitHubAPI if empty.
	API string
	// Secrets is each agent's secret, by name.
	Secrets map[string]string
	Client  *http.Client

	hashes map[string][sha256.Size]byte

	identityMu *sync.Mutex
	who        *protocol.GitIdentity
}

// EnableGitHub lets the agents act on GitHub through the control plane. An agent without
// a secret, or with an empty one, can't.
func (c *Control) EnableGitHub(g GitHub) error {
	if err := checkRepo(g.Repo); err != nil {
		return err
	}
	if g.API == "" {
		g.API = GitHubAPI
	}
	if g.Client == nil {
		g.Client = &http.Client{Timeout: githubTimeout}
	}
	var err error
	if g.hashes, err = c.hashSecrets(g.Secrets); err != nil {
		return err
	}
	g.Secrets = nil // Only the hashes are kept.
	g.identityMu = &sync.Mutex{}
	c.github = &g
	return nil
}

// hashSecrets is each agent's secret hashed, for comparing a presented one with. An
// agent without a secret, or with an empty one, has none to present.
func (c *Control) hashSecrets(secrets map[string]string) (map[string][sha256.Size]byte, error) {
	hashes := map[string][sha256.Size]byte{}
	for agent, secret := range secrets {
		if c.mailboxes[agent] == nil {
			return nil, fmt.Errorf("a secret for '%s', which is not an agent of this swarm", agent)
		}
		if secret != "" {
			hashes[agent] = sha256.Sum256([]byte(secret))
		}
	}
	return hashes, nil
}

// whoIs is the agent the secret belongs to, or "" if it is nobody's. It compares in
// constant time with every agent's, so how long it takes says nothing about which agent
// came closest.
func (g *GitHub) whoIs(secret string) string { return whoIs(g.hashes, secret) }

// whoIs is the agent whose hashed secret in hashes is secret, or "" if it is nobody's.
func whoIs(hashes map[string][sha256.Size]byte, secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	found := ""
	for agent, hash := range hashes {
		if subtle.ConstantTimeCompare(sum[:], hash[:]) == 1 {
			found = agent
		}
	}
	return found
}

// githubRequest carries out a request on GitHub for the agent whose secret it has.
func (c *Control) githubRequest(req protocol.Request) *protocol.Response {
	g := c.github
	if g == nil {
		return protocol.Fail("unauthorized: this swarm's control plane has no GitHub access")
	}
	agent := g.whoIs(req.Secret)
	if agent == "" {
		return protocol.Fail("unauthorized: the agent secret is missing or wrong")
	}
	if req.GitHub == nil {
		return protocol.Fail("bad request: missing github")
	}
	if g.Token == "" {
		return protocol.Fail("the control plane has no GitHub token")
	}
	ctx, cancel := context.WithTimeout(context.Background(), githubTimeout)
	defer cancel()
	if req.GitHub.Action == protocol.GitIdentityAction {
		who, err := g.identity(ctx)
		if err != nil {
			return protocol.Fail(err.Error())
		}
		r := protocol.OK()
		r.Identity = who
		return r
	}
	out, err := g.do(ctx, c, agent, *req.GitHub)
	if err != nil {
		return protocol.Fail(err.Error())
	}
	r := protocol.OK()
	r.Output = out
	return r
}

func (g *GitHub) do(ctx context.Context, c *Control, agent string, r protocol.GitHubRequest) (string, error) {
	needNumber := func() error {
		if r.Number < 1 {
			return errors.New("a pull request number is required")
		}
		return nil
	}
	switch r.Action {
	case protocol.PRCreate:
		return g.create(ctx, agent, r)
	case protocol.PRList:
		return g.list(ctx, r)
	case protocol.PRView:
		if err := needNumber(); err != nil {
			return "", err
		}
		return g.view(ctx, r.Number)
	case protocol.PRDiff:
		if err := needNumber(); err != nil {
			return "", err
		}
		return g.diff(ctx, r.Number)
	case protocol.PRChecks:
		if err := needNumber(); err != nil {
			return "", err
		}
		return g.checks(ctx, r.Number)
	case protocol.PRComment:
		if err := needNumber(); err != nil {
			return "", err
		}
		if strings.TrimSpace(r.Body) == "" {
			return "", errors.New("a comment needs text")
		}
		var made struct {
			HTMLURL string `json:"html_url"`
		}
		if err := g.json(ctx, "POST", fmt.Sprintf("/issues/%d/comments", r.Number), map[string]any{"body": r.Body}, &made); err != nil {
			return "", err
		}
		return fmt.Sprintf("Commented on #%d: %s\n", r.Number, made.HTMLURL), nil
	case protocol.PRReview:
		if err := needNumber(); err != nil {
			return "", err
		}
		return g.review(ctx, r)
	case protocol.PRMerge:
		if err := needNumber(); err != nil {
			return "", err
		}
		// The permission is the setup `gnat up` gave the control plane; the request says
		// nothing about it.
		if !c.setups[agent].Merge {
			return "", fmt.Errorf("agent '%s' is not allowed to merge pull requests", agent)
		}
		return g.merge(ctx, r)
	}
	return "", fmt.Errorf("unknown action '%s'", r.Action)
}

// create opens a pull request from one of the agent's own branches.
func (g *GitHub) create(ctx context.Context, agent string, r protocol.GitHubRequest) (string, error) {
	prefix, err := state.BranchPrefix(g.Swarm, agent)
	if err != nil {
		return "", err
	}
	// A head is a branch name and nothing more: "owner:branch" names a fork's, and has a
	// ':' no branch can.
	if !state.ValidBranch(r.Head) || !strings.HasPrefix(r.Head, prefix) || r.Head == prefix {
		return "", fmt.Errorf("agent '%s' may only open pull requests from its own branches, named %sNAME; got %q",
			agent, prefix, r.Head)
	}
	if strings.TrimSpace(r.Title) == "" {
		return "", errors.New("a pull request needs a title")
	}
	base := r.Base
	if base == "" {
		var repo struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := g.json(ctx, "GET", "", nil, &repo); err != nil {
			return "", err
		}
		base = repo.DefaultBranch
	}
	if !state.ValidBranch(base) {
		return "", fmt.Errorf("%q isn't a branch to merge into", base)
	}
	var made struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	err = g.json(ctx, "POST", "/pulls", map[string]any{
		"title": r.Title, "body": r.Body, "head": r.Head, "base": base, "draft": r.Draft,
		"maintainer_can_modify": false,
	}, &made)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Created pull request #%d: %s\n", made.Number, made.HTMLURL), nil
}

type pullJSON struct {
	Number   int     `json:"number"`
	Title    string  `json:"title"`
	Body     *string `json:"body"`
	HTMLURL  string  `json:"html_url"`
	State    string  `json:"state"`
	Draft    bool    `json:"draft"`
	MergedAt *string `json:"merged_at"`
	Merged   bool    `json:"merged"`
	// Mergeable is null until GitHub has worked it out.
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Additions    int `json:"additions"`
	Deletions    int `json:"deletions"`
	ChangedFiles int `json:"changed_files"`
}

func (p pullJSON) state() string {
	if p.MergedAt != nil || p.Merged {
		return "merged"
	}
	return p.State
}

func (g *GitHub) list(ctx context.Context, r protocol.GitHubRequest) (string, error) {
	state, filter := r.State, ""
	switch state {
	case "":
		state = "open"
	case "open", "closed", "all":
	case "merged":
		state, filter = "closed", "merged"
	default:
		return "", fmt.Errorf("unknown state %q: use open, closed, merged, or all", r.State)
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 30
	}
	limit = min(limit, maxListed)
	query := url.Values{"state": {state}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {fmt.Sprint(maxListed)}}
	var pulls []pullJSON
	if err := g.json(ctx, "GET", "/pulls?"+query.Encode(), nil, &pulls); err != nil {
		return "", err
	}
	var out strings.Builder
	n := 0
	for _, p := range pulls {
		if filter == "merged" && p.state() != "merged" {
			continue
		}
		if n++; n > limit {
			break
		}
		draft := ""
		if p.Draft {
			draft = ", draft"
		}
		fmt.Fprintf(&out, "#%d\t%s\t%s\t%s → %s\t%s (%s%s)\n", p.Number, p.state(), p.Title, p.Head.Ref, p.Base.Ref, p.User.Login, p.HTMLURL, draft)
	}
	if n == 0 {
		return "No pull requests.\n", nil
	}
	return out.String(), nil
}

type userJSON struct {
	Login string `json:"login"`
}

func (g *GitHub) view(ctx context.Context, number int) (string, error) {
	var p pullJSON
	if err := g.json(ctx, "GET", fmt.Sprintf("/pulls/%d", number), nil, &p); err != nil {
		return "", err
	}
	var reviews []struct {
		User        userJSON `json:"user"`
		State       string   `json:"state"`
		Body        string   `json:"body"`
		SubmittedAt string   `json:"submitted_at"`
	}
	var comments []struct {
		User      userJSON `json:"user"`
		Body      string   `json:"body"`
		CreatedAt string   `json:"created_at"`
	}
	var inline []struct {
		User      userJSON `json:"user"`
		Body      string   `json:"body"`
		Path      string   `json:"path"`
		Line      *int     `json:"line"`
		CreatedAt string   `json:"created_at"`
	}
	page := fmt.Sprintf("?per_page=%d", maxItems)
	for _, fetch := range []struct {
		path string
		into any
	}{
		{fmt.Sprintf("/pulls/%d/reviews", number), &reviews},
		{fmt.Sprintf("/issues/%d/comments", number), &comments},
		{fmt.Sprintf("/pulls/%d/comments", number), &inline},
	} {
		if err := g.json(ctx, "GET", fetch.path+page, nil, fetch.into); err != nil {
			return "", err
		}
	}

	var out strings.Builder
	state := p.state()
	if p.Draft {
		state += ", draft"
	}
	fmt.Fprintf(&out, "#%d %s [%s]\n%s\n%s → %s, opened by %s\n", p.Number, p.Title, state, p.HTMLURL, p.Head.Ref, p.Base.Ref, p.User.Login)
	fmt.Fprintf(&out, "%d changed files, +%d -%d\n", p.ChangedFiles, p.Additions, p.Deletions)
	switch {
	case p.state() != "open":
	case p.Mergeable == nil:
		out.WriteString("Mergeable: not yet known\n")
	case *p.Mergeable:
		out.WriteString("Mergeable: yes\n")
	default:
		fmt.Fprintf(&out, "Mergeable: no (%s)\n", p.MergeableState)
	}
	if p.Body != nil && strings.TrimSpace(*p.Body) != "" {
		fmt.Fprintf(&out, "\n%s\n", strings.TrimSpace(*p.Body))
	}
	if len(reviews) > 0 {
		out.WriteString("\nReviews:\n")
		for _, r := range reviews {
			fmt.Fprintf(&out, "- %s %s (%s)\n%s", r.User.Login, strings.ToLower(strings.ReplaceAll(r.State, "_", " ")), r.SubmittedAt, indent(r.Body))
		}
	}
	if len(comments) > 0 {
		out.WriteString("\nComments:\n")
		for _, c := range comments {
			fmt.Fprintf(&out, "- %s (%s)\n%s", c.User.Login, c.CreatedAt, indent(c.Body))
		}
	}
	if len(inline) > 0 {
		out.WriteString("\nInline comments:\n")
		for _, c := range inline {
			where := c.Path
			if c.Line != nil {
				where += fmt.Sprintf(":%d", *c.Line)
			}
			fmt.Fprintf(&out, "- %s on %s (%s)\n%s", c.User.Login, where, c.CreatedAt, indent(c.Body))
		}
	}
	return out.String(), nil
}

// indent is text set under a list item, ending in a newline, or nothing if it is blank.
func indent(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return "    " + strings.ReplaceAll(text, "\n", "\n    ") + "\n"
}

func (g *GitHub) diff(ctx context.Context, number int) (string, error) {
	body, err := g.request(ctx, "GET", fmt.Sprintf("/pulls/%d", number), nil, "application/vnd.github.diff")
	if err != nil {
		return "", err
	}
	if len(body) > maxDiffBytes {
		body = append(body[:maxDiffBytes:maxDiffBytes], "\n[diff truncated]\n"...)
	}
	return string(body), nil
}

func (g *GitHub) checks(ctx context.Context, number int) (string, error) {
	var p pullJSON
	if err := g.json(ctx, "GET", fmt.Sprintf("/pulls/%d", number), nil, &p); err != nil {
		return "", err
	}
	// The head commit's SHA comes from GitHub, but is checked before it goes in a path.
	if !regexp.MustCompile(`^[0-9a-f]{7,64}\z`).MatchString(p.Head.SHA) {
		return "", fmt.Errorf("GitHub gave an unusable head commit %q", p.Head.SHA)
	}
	var runs struct {
		CheckRuns []struct {
			Name       string  `json:"name"`
			Status     string  `json:"status"`
			Conclusion *string `json:"conclusion"`
			HTMLURL    string  `json:"html_url"`
		} `json:"check_runs"`
	}
	var combined struct {
		Statuses []struct {
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := g.json(ctx, "GET", fmt.Sprintf("/commits/%s/check-runs?per_page=%d", p.Head.SHA, maxItems), nil, &runs); err != nil {
		return "", err
	}
	if err := g.json(ctx, "GET", fmt.Sprintf("/commits/%s/status?per_page=%d", p.Head.SHA, maxItems), nil, &combined); err != nil {
		return "", err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Checks for #%d at %s\n", number, p.Head.SHA[:min(7, len(p.Head.SHA))])
	for _, r := range runs.CheckRuns {
		result := r.Status
		if r.Conclusion != nil {
			result = *r.Conclusion
		}
		fmt.Fprintf(&out, "%s\t%s\t%s\n", result, r.Name, r.HTMLURL)
	}
	for _, s := range combined.Statuses {
		fmt.Fprintf(&out, "%s\t%s\t%s\n", s.State, s.Context, s.TargetURL)
	}
	if len(runs.CheckRuns) == 0 && len(combined.Statuses) == 0 {
		out.WriteString("No checks reported.\n")
	}
	return out.String(), nil
}

func (g *GitHub) review(ctx context.Context, r protocol.GitHubRequest) (string, error) {
	var event string
	switch r.Review {
	case protocol.ReviewApprove:
		event = "APPROVE"
	case protocol.ReviewRequestChanges:
		event = "REQUEST_CHANGES"
	case protocol.ReviewComment:
		event = "COMMENT"
	default:
		return "", fmt.Errorf("unknown review %q: use approve, request_changes, or comment", r.Review)
	}
	if event != "APPROVE" && strings.TrimSpace(r.Body) == "" {
		return "", errors.New("a review that doesn't approve needs text")
	}
	body := map[string]any{"event": event}
	if r.Body != "" {
		body["body"] = r.Body
	}
	var made struct {
		HTMLURL string `json:"html_url"`
	}
	if err := g.json(ctx, "POST", fmt.Sprintf("/pulls/%d/reviews", r.Number), body, &made); err != nil {
		return "", err
	}
	return fmt.Sprintf("Reviewed #%d (%s): %s\n", r.Number, strings.ToLower(strings.ReplaceAll(event, "_", " ")), made.HTMLURL), nil
}

func (g *GitHub) merge(ctx context.Context, r protocol.GitHubRequest) (string, error) {
	method := r.Method
	switch method {
	case "":
		method = protocol.MergeMerge
	case protocol.MergeMerge, protocol.MergeSquash, protocol.MergeRebase:
	default:
		return "", fmt.Errorf("unknown merge method %q: use merge, squash, or rebase", r.Method)
	}
	var merged struct {
		Merged  bool   `json:"merged"`
		Message string `json:"message"`
	}
	if err := g.json(ctx, "PUT", fmt.Sprintf("/pulls/%d/merge", r.Number), map[string]any{"merge_method": method}, &merged); err != nil {
		return "", err
	}
	if !merged.Merged {
		return "", fmt.Errorf("#%d wasn't merged: %s", r.Number, merged.Message)
	}
	return fmt.Sprintf("Merged #%d (%s)\n", r.Number, method), nil
}

// json makes a request on the repository and decodes the answer into out.
func (g *GitHub) json(ctx context.Context, method, path string, body, out any) error {
	data, err := g.request(ctx, method, path, body, "application/vnd.github+json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("GitHub's answer: %w", err)
	}
	return nil
}

// identity is the account the token belongs to, as git commits as it. GitHub is asked
// once.
func (g *GitHub) identity(ctx context.Context) (*protocol.GitIdentity, error) {
	g.identityMu.Lock()
	defer g.identityMu.Unlock()
	if g.who != nil {
		return g.who, nil
	}
	data, err := g.send(ctx, "GET", "/user", nil, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	var user struct {
		Name  *string `json:"name"`
		Login string  `json:"login"`
		ID    int64   `json:"id"`
	}
	if err := json.Unmarshal(data, &user); err != nil || user.Login == "" {
		return nil, errors.New("GitHub's answer for the user wasn't usable")
	}
	name := user.Login
	if user.Name != nil && *user.Name != "" {
		name = *user.Name
	}
	g.who = &protocol.GitIdentity{Name: name, Email: fmt.Sprintf("%d+%s@users.noreply.github.com", user.ID, user.Login)}
	return g.who, nil
}

// request makes a request on the repository, whose path comes after /repos/OWNER/NAME.
func (g *GitHub) request(ctx context.Context, method, path string, body any, accept string) ([]byte, error) {
	return g.send(ctx, method, "/repos/"+g.Repo+path, body, accept)
}

// send makes a request to the API, whose path is the whole of it.
func (g *GitHub) send(ctx context.Context, method, path string, body any, accept string) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(g.API, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+g.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDiffBytes+1))
	if err != nil {
		return nil, fmt.Errorf("GitHub: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("GitHub answered %s%s", resp.Status, githubMessage(data))
	}
	return data, nil
}

// githubMessage is what GitHub said was wrong, as ": message (detail, ...)", or nothing.
func githubMessage(data []byte) string {
	var e struct {
		Message string `json:"message"`
		Errors  []any  `json:"errors"`
	}
	if json.Unmarshal(data, &e) != nil || e.Message == "" {
		return ""
	}
	out := ": " + e.Message
	if len(e.Errors) > 0 {
		var details []string
		for _, d := range e.Errors {
			if s, ok := d.(string); ok {
				details = append(details, s)
			} else if m, err := json.Marshal(d); err == nil {
				details = append(details, string(m))
			}
		}
		out += " (" + strings.Join(details, "; ") + ")"
	}
	if len(out) > 500 {
		out = out[:500] + "…"
	}
	return out
}
