package control

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/state"
)

const fakeToken = "gh-token"

// fakeGitHub is GitHub's API for the repository o/r, recording what it was asked.
type fakeGitHub struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	requests []fakeRequest
	// answers is what each "METHOD PATH" answers with, as JSON, or as it is if a string.
	answers map[string]any
	// status is the status each "METHOD PATH" answers with, 200 if absent.
	status map[string]int
}

type fakeRequest struct {
	method, path, query, auth, accept string
	body                              map[string]any
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, answers: map[string]any{}, status: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, fakeRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Accept"), body})
		key := r.Method + " " + r.URL.Path
		if code := f.status[key]; code != 0 {
			w.WriteHeader(code)
		}
		switch answer := f.answers[key].(type) {
		case nil:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"Not Found"}`)
		case string:
			io.WriteString(w, answer)
		default:
			json.NewEncoder(w).Encode(answer)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitHub) asked() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRequest(nil), f.requests...)
}

// githubPlane is a control plane for the swarm "demo" of alpha, beta, and merger, with
// GitHub access through f. Only merger may merge.
func githubPlane(t *testing.T, f *fakeGitHub) *plane {
	t.Helper()
	p := startPlane(t, t.TempDir(), "alpha", "beta", "merger")
	p.control.setups = map[string]state.Agent{"alpha": {Name: "alpha"}, "beta": {Name: "beta"}, "merger": {Name: "merger", Merge: true}}
	err := p.control.EnableGitHub(GitHub{Swarm: "demo", Repo: "o/r", Token: fakeToken, API: f.server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func gh(secret string, r protocol.GitHubRequest) protocol.Request {
	return protocol.Request{Op: protocol.OpGitHub, Secret: secret, GitHub: &r}
}

// refused is a request the control plane must refuse, with a message containing want.
func (p *plane) refused(req protocol.Request, want string) {
	p.t.Helper()
	resp, err := p.request(req)
	if err == nil {
		p.t.Fatalf("%+v was carried out: %+v", req.GitHub, resp)
	}
	if !strings.Contains(err.Error(), want) {
		p.t.Fatalf("%+v: got %q, want it to contain %q", req.GitHub, err, want)
	}
}

func TestGitHubRefusesAMissingForgedOrForeignSecret(t *testing.T) {
	f := newFakeGitHub(t)
	p := githubPlane(t, f)
	list := protocol.GitHubRequest{Action: protocol.PRList}
	create := protocol.GitHubRequest{Action: protocol.PRCreate, Title: "t", Head: "demo/beta/x", Base: "main"}

	p.refused(gh("", list), "unauthorized")
	p.refused(gh("forged", list), "unauthorized")
	p.refused(gh("alpha-secre", list), "unauthorized")
	p.refused(gh("alpha-secret ", list), "unauthorized")
	p.refused(protocol.Request{Op: protocol.OpGitHub, Agent: "alpha", GitHub: &list}, "unauthorized")
	// Naming an agent proves nothing; the secret is what says who is asking.
	p.refused(protocol.Request{Op: protocol.OpGitHub, Agent: "alpha", Secret: "x", GitHub: &list}, "unauthorized")
	if got := f.asked(); len(got) != 0 {
		t.Fatalf("GitHub was asked: %+v", got)
	}

	// Another agent's secret is that agent: beta's secret can't open alpha's branches,
	// whoever the request says it is.
	forged := protocol.Request{Op: protocol.OpGitHub, Agent: "alpha", Secret: "beta-secret", GitHub: &create}
	forged.GitHub.Head = "demo/alpha/x"
	p.refused(forged, "may only open pull requests from its own branches")
	if got := f.asked(); len(got) != 0 {
		t.Fatalf("GitHub was asked: %+v", got)
	}
}

func TestGitHubIsUnavailableUntilEnabled(t *testing.T) {
	p := startPlane(t, t.TempDir())
	p.refused(gh("anything", protocol.GitHubRequest{Action: protocol.PRList}), "unauthorized")
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRList}), "no GitHub access")
}

func TestEnableGitHubChecksItsArguments(t *testing.T) {
	p := startPlane(t, t.TempDir())
	for _, repo := range []string{"", "r", "o/r/x", "o/r?x=1", "o/../r", "o/r\n", "../..", "./r", "o/.."} {
		if err := p.control.EnableGitHub(GitHub{Swarm: "demo", Repo: repo}); err == nil {
			t.Errorf("accepted repository %q", repo)
		}
	}
	if err := p.control.SetSecrets(map[string]string{"ghost": "x"}); err == nil {
		t.Error("accepted a secret for an agent that isn't in the swarm")
	}
	// An agent whose secret is empty has none.
	if err := p.control.SetSecrets(map[string]string{"alpha": ""}); err != nil {
		t.Fatal(err)
	}
	if err := p.control.EnableGitHub(GitHub{Swarm: "demo", Repo: "o/r"}); err != nil {
		t.Fatal(err)
	}
	p.refused(gh("", protocol.GitHubRequest{Action: protocol.PRList}), "unauthorized")
}

func TestGitHubCreateOpensPullRequestsOnlyFromTheAgentsOwnBranches(t *testing.T) {
	f := newFakeGitHub(t)
	f.answers["GET /repos/o/r"] = map[string]any{"default_branch": "trunk"}
	f.answers["POST /repos/o/r/pulls"] = map[string]any{"number": 7, "html_url": "https://github.com/o/r/pull/7"}
	p := githubPlane(t, f)
	create := func(head string) protocol.Request {
		return gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRCreate, Title: "t", Body: "b", Head: head})
	}
	for _, head := range []string{
		"main",                     // not under a prefix
		"demo/alpha/",              // the prefix alone
		"demo/beta/x",              // another agent's
		"demo/alphabet/x",          // a longer name
		"demo/alpha",               // no slash
		"other/alpha/x",            // another swarm's
		"demo/alpha/../beta/x",     // out of the prefix
		"demo/alpha/x..y",          // not a ref
		"demo/alpha/x.lock",        // not a ref
		"demo/alpha/x y",           // not a ref
		"someone:demo/alpha/x",     // a fork's branch
		"o:demo/alpha/x",           // the repository's owner, as a fork would spell it
		"refs/heads/demo/alpha/x",  // not a branch name
		"Demo/alpha/x",             // case matters
		" demo/alpha/x",            // padding
		"demo/alpha/x\nbase=main",  // control characters
		"-demo/alpha/x",            // an option
		"demo/alpha/x:refs/pull/1", // a refspec
		"demo/alpha/x/",            // trailing slash
		"demo/alpha/x@{1}",         // a reflog
		"demo/alpha/\\x",           // backslash
	} {
		p.refused(create(head), "may only open pull requests from its own branches")
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatalf("GitHub was asked: %+v", got)
	}

	resp := p.must(create("demo/alpha/fix-1"))
	if resp.Output != "Created pull request #7: https://github.com/o/r/pull/7\n" {
		t.Errorf("got %q", resp.Output)
	}
	got := f.asked()
	if len(got) != 2 || got[0].path != "/repos/o/r" || got[1].path != "/repos/o/r/pulls" || got[1].method != "POST" {
		t.Fatalf("asked %+v", got)
	}
	body := got[1].body
	// The head is the bare branch, so GitHub takes it from the repository itself.
	if body["head"] != "demo/alpha/fix-1" || body["base"] != "trunk" || body["title"] != "t" || body["body"] != "b" || body["draft"] != false {
		t.Errorf("body %v", body)
	}
	for _, r := range got {
		if r.auth != "Bearer "+fakeToken {
			t.Errorf("%s %s authorized with %q", r.method, r.path, r.auth)
		}
	}
}

func TestGitHubCreateRefusesAgentsWhoseNamesMakeInvalidRefs(t *testing.T) {
	f := newFakeGitHub(t)
	p := startPlane(t, t.TempDir(), "a..b", "x.lock", "ok", "dot.")
	if err := p.control.SetSecrets(map[string]string{"a..b": "s1", "x.lock": "s2", "ok": "s3", "dot.": "s4"}); err != nil {
		t.Fatal(err)
	}
	if err := p.control.EnableGitHub(GitHub{Swarm: "demo", Repo: "o/r", Token: fakeToken, API: f.server.URL}); err != nil {
		t.Fatal(err)
	}
	for agent, secret := range map[string]string{"a..b": "s1", "x.lock": "s2", "dot.": "s4"} {
		p.refused(gh(secret, protocol.GitHubRequest{Action: protocol.PRCreate, Title: "t", Head: "demo/" + agent + "/x", Base: "main"}),
			"can't name branches")
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatalf("GitHub was asked: %+v", got)
	}
	f.answers["POST /repos/o/r/pulls"] = map[string]any{"number": 1, "html_url": "u"}
	p.must(gh("s3", protocol.GitHubRequest{Action: protocol.PRCreate, Title: "t", Head: "demo/ok/x", Base: "main"}))

	// A swarm whose name makes invalid refs gets nothing either.
	q := startPlane(t, t.TempDir(), "ok")
	if err := q.control.SetSecrets(map[string]string{"ok": "s"}); err != nil {
		t.Fatal(err)
	}
	if err := q.control.EnableGitHub(GitHub{Swarm: "my..swarm", Repo: "o/r", Token: fakeToken, API: f.server.URL}); err != nil {
		t.Fatal(err)
	}
	q.refused(gh("s", protocol.GitHubRequest{Action: protocol.PRCreate, Title: "t", Head: "my..swarm/ok/x", Base: "main"}), "can't name branches")
}

func TestGitHubCreateNeedsATitleAndAValidBase(t *testing.T) {
	f := newFakeGitHub(t)
	p := githubPlane(t, f)
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRCreate, Head: "demo/alpha/x", Base: "main"}), "needs a title")
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRCreate, Title: "t", Head: "demo/alpha/x", Base: "fork:main"}), "isn't a branch")
	if got := f.asked(); len(got) != 0 {
		t.Fatalf("GitHub was asked: %+v", got)
	}
}

func TestGitHubMergeNeedsThePermission(t *testing.T) {
	f := newFakeGitHub(t)
	f.answers["PUT /repos/o/r/pulls/5/merge"] = map[string]any{"merged": true, "message": "ok"}
	p := githubPlane(t, f)
	merge := func(secret string) protocol.Request {
		return gh(secret, protocol.GitHubRequest{Action: protocol.PRMerge, Number: 5})
	}
	p.refused(merge("alpha-secret"), "not allowed to merge")
	p.refused(merge("beta-secret"), "not allowed to merge")
	// A request that says the agent may changes nothing: the permission is control's.
	forged := merge("alpha-secret")
	forged.Agent = "merger"
	p.refused(forged, "not allowed to merge")
	if got := f.asked(); len(got) != 0 {
		t.Fatalf("GitHub was asked: %+v", got)
	}

	resp := p.must(merge("merger-secret"))
	if resp.Output != "Merged #5 (merge)\n" {
		t.Errorf("got %q", resp.Output)
	}
	p.must(gh("merger-secret", protocol.GitHubRequest{Action: protocol.PRMerge, Number: 5, Method: protocol.MergeSquash}))
	got := f.asked()
	if len(got) != 2 || got[0].body["merge_method"] != "merge" || got[1].body["merge_method"] != "squash" {
		t.Fatalf("asked %+v", got)
	}
	p.refused(gh("merger-secret", protocol.GitHubRequest{Action: protocol.PRMerge, Number: 5, Method: "fast-forward"}), "unknown merge method")
	p.refused(gh("merger-secret", protocol.GitHubRequest{Action: protocol.PRMerge}), "number is required")
}

// pull is a pull request as GitHub answers for it, from head in the repository repo.
func pull(number int, head, repo, state string) map[string]any {
	headRepo := any(nil)
	if repo != "" {
		headRepo = map[string]any{"full_name": repo}
	}
	owner, _, _ := strings.Cut(repo, "/")
	return map[string]any{
		"number": number, "state": state, "html_url": "https://github.com/o/r/pull/" + fmt.Sprint(number),
		"head": map[string]any{"ref": head, "label": owner + ":" + head, "repo": headRepo},
	}
}

func TestGitHubClosesAndEditsOnlyTheAgentsOwnPullRequests(t *testing.T) {
	f := newFakeGitHub(t)
	f.answers["GET /repos/o/r/pulls/5"] = pull(5, "demo/alpha/x", "o/r", "open")
	f.answers["PATCH /repos/o/r/pulls/5"] = pull(5, "demo/alpha/x", "o/r", "closed")
	p := githubPlane(t, f)
	closing := gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRClose, Number: 5})
	if resp := p.must(closing); resp.Output != "Closed #5: https://github.com/o/r/pull/5\n" {
		t.Errorf("got %q", resp.Output)
	}
	editing := gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PREdit, Number: 5, Title: "New", Body: "Text"})
	if resp := p.must(editing); resp.Output != "Edited #5: https://github.com/o/r/pull/5\n" {
		t.Errorf("got %q", resp.Output)
	}
	p.must(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PREdit, Number: 5, Body: "Only text"}))
	got := f.asked()
	if len(got) != 6 {
		t.Fatalf("asked %+v", got)
	}
	for i, want := range []map[string]any{{"state": "closed"}, {"title": "New", "body": "Text"}, {"body": "Only text"}} {
		check, change := got[2*i], got[2*i+1]
		if check.method != "GET" || change.method != "PATCH" || change.path != "/repos/o/r/pulls/5" || !reflect.DeepEqual(change.body, want) {
			t.Errorf("asked %+v then %+v, want a PATCH of %v", check, change, want)
		}
	}
}

func TestGitHubRefusesToCloseOrEditOthersPullRequests(t *testing.T) {
	f := newFakeGitHub(t)
	f.answers["GET /repos/o/r/pulls/5"] = pull(5, "demo/beta/x", "o/r", "open")
	// A fork's branch named like one of alpha's isn't alpha's.
	f.answers["GET /repos/o/r/pulls/6"] = pull(6, "demo/alpha/x", "someone/r", "open")
	// Nor is one whose fork is gone.
	f.answers["GET /repos/o/r/pulls/7"] = pull(7, "demo/alpha/x", "", "open")
	f.answers["GET /repos/o/r/pulls/8"] = pull(8, "demo/alpha/", "o/r", "open")
	f.answers["GET /repos/o/r/pulls/9"] = pull(9, "main", "o/r", "open")
	f.answers["GET /repos/o/r/pulls/10"] = pull(10, "demo/alphabet/x", "o/r", "open")
	p := githubPlane(t, f)
	for _, n := range []int{5, 6, 7, 8, 9, 10} {
		for _, r := range []protocol.GitHubRequest{
			{Action: protocol.PRClose, Number: n}, {Action: protocol.PREdit, Number: n, Title: "t"},
			{Action: protocol.PRReady, Number: n},
		} {
			p.refused(gh("alpha-secret", r), "may only close, edit, or mark ready pull requests from its own branches, named demo/alpha/NAME")
		}
	}
	// The secret says who is asking, not the request, and merging rights don't help.
	forged := gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRClose, Number: 6})
	forged.Agent = "alpha"
	p.refused(forged, "agent 'beta' may only close, edit, or mark ready")
	p.refused(gh("merger-secret", protocol.GitHubRequest{Action: protocol.PRClose, Number: 5}), "agent 'merger' may only close, edit, or mark ready")
	for _, r := range f.asked() {
		if r.method != "GET" {
			t.Fatalf("GitHub was asked to %s %s", r.method, r.path)
		}
	}
}

func TestGitHubCloseAndEditCheckTheirArguments(t *testing.T) {
	f := newFakeGitHub(t)
	f.answers["GET /repos/o/r/pulls/5"] = pull(5, "demo/alpha/x", "o/r", "closed")
	p := githubPlane(t, f)
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRClose}), "number is required")
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PREdit, Title: "t"}), "number is required")
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PREdit, Number: 5}), "needs a title or a body")
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PREdit, Number: 5, Title: "  "}), "needs a title")
	if got := f.asked(); len(got) != 0 {
		t.Fatalf("GitHub was asked: %+v", got)
	}
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRClose, Number: 5}), "#5 is already closed")
	f.answers["GET /repos/o/r/pulls/5"] = map[string]any{"state": "closed", "merged_at": "2026-10-22T00:00:00Z",
		"head": map[string]any{"ref": "demo/alpha/x", "repo": map[string]any{"full_name": "o/r"}}}
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRClose, Number: 5}), "#5 is already merged")
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRClose, Number: 4}), "404 Not Found")
}

func TestGitHubMarksTheAgentsOwnDraftsReady(t *testing.T) {
	f := newFakeGitHub(t)
	draft := pull(5, "demo/alpha/x", "o/r", "open")
	draft["draft"], draft["node_id"] = true, "PR_kw5"
	f.answers["GET /repos/o/r/pulls/5"] = draft
	f.answers["POST /graphql"] = map[string]any{"data": map[string]any{}}
	p := githubPlane(t, f)
	ready := gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRReady, Number: 5})
	if resp := p.must(ready); resp.Output != "Marked #5 ready for review: https://github.com/o/r/pull/5\n" {
		t.Errorf("got %q", resp.Output)
	}
	got := f.asked()
	if len(got) != 2 || got[1].method != "POST" || got[1].path != "/graphql" || got[1].auth != "Bearer "+fakeToken {
		t.Fatalf("asked %+v", got)
	}
	query, _ := got[1].body["query"].(string)
	if !strings.Contains(query, "markPullRequestReadyForReview") || !reflect.DeepEqual(got[1].body["variables"], map[string]any{"id": "PR_kw5"}) {
		t.Errorf("asked GraphQL %v", got[1].body)
	}

	f.answers["POST /graphql"] = map[string]any{"errors": []map[string]any{{"message": "Pull request is not a draft"}}}
	p.refused(ready, "GitHub refused: Pull request is not a draft")

	f.answers["GET /repos/o/r/pulls/5"] = pull(5, "demo/alpha/x", "o/r", "open")
	p.refused(ready, "#5 isn't a draft")
	f.answers["GET /repos/o/r/pulls/5"] = pull(5, "demo/alpha/x", "o/r", "closed")
	p.refused(ready, "#5 is closed")
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRReady}), "number is required")
}

func TestGitHubMergeReportsWhatGitHubRefuses(t *testing.T) {
	f := newFakeGitHub(t)
	f.status["PUT /repos/o/r/pulls/5/merge"] = http.StatusMethodNotAllowed
	f.answers["PUT /repos/o/r/pulls/5/merge"] = map[string]any{"message": "Pull Request is not mergeable"}
	p := githubPlane(t, f)
	p.refused(gh("merger-secret", protocol.GitHubRequest{Action: protocol.PRMerge, Number: 5}), "405 Method Not Allowed: Pull Request is not mergeable")
	f.status["PUT /repos/o/r/pulls/5/merge"] = 0
	f.answers["PUT /repos/o/r/pulls/5/merge"] = map[string]any{"merged": false, "message": "head changed"}
	p.refused(gh("merger-secret", protocol.GitHubRequest{Action: protocol.PRMerge, Number: 5}), "#5 wasn't merged: head changed")
}

func TestGitHubOnlyEverTouchesTheSwarmsRepository(t *testing.T) {
	f := newFakeGitHub(t)
	f.answers["GET /repos/o/r"] = map[string]any{"default_branch": "main"}
	p := githubPlane(t, f)
	for _, a := range []protocol.GitHubRequest{
		{Action: protocol.PRList}, {Action: protocol.PRView, Number: 1}, {Action: protocol.PRDiff, Number: 1},
		{Action: protocol.PRChecks, Number: 1}, {Action: protocol.PRComment, Number: 1, Body: "x"},
		{Action: protocol.PRReview, Number: 1, Review: protocol.ReviewApprove},
		{Action: protocol.PRCreate, Title: "t", Head: "demo/merger/x"}, // Asks for the default branch.
		{Action: protocol.PRCreate, Title: "t", Head: "demo/merger/x", Base: "main"}, {Action: protocol.PRMerge, Number: 1},
		{Action: protocol.PRClose, Number: 1}, {Action: protocol.PREdit, Number: 1, Title: "t"},
		{Action: protocol.PRReady, Number: 1},
	} {
		_, _ = p.request(gh("merger-secret", a)) // GitHub answers 404; only where it was asked matters.
	}
	got := f.asked()
	if len(got) != 13 {
		t.Fatalf("asked %+v", got)
	}
	defaultBranch := false
	for _, r := range got {
		// The repository itself, for its default branch, or something in it.
		if r.path == "/repos/o/r" {
			defaultBranch = r.method == "GET"
		} else if !strings.HasPrefix(r.path, "/repos/o/r/") {
			t.Errorf("%s %s", r.method, r.path)
		}
	}
	if !defaultBranch {
		t.Error("the default branch fetch wasn't among the requests")
	}
}

func TestGitHubRefusesWhatItDoesNotKnow(t *testing.T) {
	f := newFakeGitHub(t)
	p := githubPlane(t, f)
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: "repo_delete"}), "unknown action")
	p.refused(gh("alpha-secret", protocol.GitHubRequest{}), "unknown action")
	p.refused(protocol.Request{Op: protocol.OpGitHub, Secret: "alpha-secret"}, "missing github")
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRView}), "number is required")
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRList, State: "everything"}), "unknown state")
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRComment, Number: 1, Body: " "}), "needs text")
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRReview, Number: 1, Review: "dismiss"}), "unknown review")
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRReview, Number: 1, Review: protocol.ReviewRequestChanges}), "needs text")
	if got := f.asked(); len(got) != 0 {
		t.Fatalf("GitHub was asked: %+v", got)
	}
}

func TestGitHubListsPullRequests(t *testing.T) {
	f := newFakeGitHub(t)
	f.answers["GET /repos/o/r/pulls"] = []map[string]any{
		{"number": 3, "title": "Open one", "html_url": "u3", "state": "open", "draft": true, "head": map[string]any{"ref": "demo/alpha/a"},
			"base": map[string]any{"ref": "main"}, "user": map[string]any{"login": "kyle"}},
		{"number": 2, "title": "Merged one", "html_url": "u2", "state": "closed", "merged_at": "2026-01-01T00:00:00Z",
			"head": map[string]any{"ref": "demo/beta/b"}, "base": map[string]any{"ref": "main"}, "user": map[string]any{"login": "kyle"},
			"labels": []map[string]any{{"name": "gnat:approved"}, {"name": "bug"}}},
		{"number": 1, "title": "Closed one", "html_url": "u1", "state": "closed",
			"head": map[string]any{"ref": "c"}, "base": map[string]any{"ref": "main"}, "user": map[string]any{"login": "kyle"}},
	}
	p := githubPlane(t, f)
	all := p.must(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRList, State: "all"})).Output
	want := "#3\topen\tOpen one\tdemo/alpha/a → main\tkyle (u3, draft)\n" +
		"#2\tmerged\tMerged one\tdemo/beta/b → main\tkyle (u2, gnat:approved, bug)\n" +
		"#1\tclosed\tClosed one\tc → main\tkyle (u1)\n"
	if all != want {
		t.Errorf("got:\n%s\nwant:\n%s", all, want)
	}
	if q := f.asked()[0].query; !strings.Contains(q, "state=all") {
		t.Errorf("query %q", q)
	}
	merged := p.must(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRList, State: "merged"})).Output
	if !strings.HasPrefix(merged, "#2\tmerged") || strings.Count(merged, "\n") != 1 {
		t.Errorf("merged: %q", merged)
	}
	if q := f.asked()[1].query; !strings.Contains(q, "state=closed") {
		t.Errorf("query %q", q)
	}
	limited := p.must(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRList, State: "all", Limit: 1})).Output
	if strings.Count(limited, "\n") != 1 {
		t.Errorf("limited: %q", limited)
	}
}

func TestGitHubViewShowsCommentsAndReviews(t *testing.T) {
	f := newFakeGitHub(t)
	f.answers["GET /repos/o/r/pulls/4"] = map[string]any{
		"number": 4, "title": "Add x", "body": "Does x.\nTwice.", "html_url": "u4", "state": "open",
		"mergeable": false, "mergeable_state": "dirty", "changed_files": 2, "additions": 10, "deletions": 3,
		"head": map[string]any{"ref": "demo/alpha/x", "sha": "abc1234"}, "base": map[string]any{"ref": "main"},
		"user": map[string]any{"login": "kyle"}, "labels": []map[string]any{{"name": "gnat:changes-requested"}},
	}
	f.answers["GET /repos/o/r/pulls/4/reviews"] = []map[string]any{
		{"user": map[string]any{"login": "rev"}, "state": "CHANGES_REQUESTED", "body": "Fix y.", "submitted_at": "t1"},
	}
	f.answers["GET /repos/o/r/issues/4/comments"] = []map[string]any{
		{"user": map[string]any{"login": "kyle"}, "body": "Thanks\nsoon", "created_at": "t2"},
	}
	f.answers["GET /repos/o/r/pulls/4/comments"] = []map[string]any{
		{"user": map[string]any{"login": "rev"}, "body": "nit", "path": "x.py", "line": 12, "created_at": "t3"},
	}
	p := githubPlane(t, f)
	got := p.must(gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRView, Number: 4})).Output
	want := `#4 Add x [open]
u4
demo/alpha/x → main, opened by kyle
2 changed files, +10 -3
Labels: gnat:changes-requested
Mergeable: no (dirty)

Does x.
Twice.

Reviews:
- rev changes requested (t1)
    Fix y.

Comments:
- kyle (t2)
    Thanks
    soon

Inline comments:
- rev on x.py:12 (t3)
    nit
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestGitHubDiffChecksCommentAndReview(t *testing.T) {
	f := newFakeGitHub(t)
	f.answers["GET /repos/o/r/pulls/4"] = "diff --git a/x b/x\n"
	p := githubPlane(t, f)
	if got := p.must(gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRDiff, Number: 4})).Output; got != "diff --git a/x b/x\n" {
		t.Errorf("diff %q", got)
	}
	if a := f.asked()[0].accept; a != "application/vnd.github.diff" {
		t.Errorf("accept %q", a)
	}

	f.answers["GET /repos/o/r/pulls/4"] = map[string]any{"number": 4, "head": map[string]any{"sha": "abc1234def"}}
	f.answers["GET /repos/o/r/commits/abc1234def/check-runs"] = map[string]any{"check_runs": []map[string]any{
		{"name": "test", "status": "completed", "conclusion": "success", "html_url": "ur"},
		{"name": "lint", "status": "in_progress", "html_url": "ul"},
	}}
	f.answers["GET /repos/o/r/commits/abc1234def/status"] = map[string]any{"statuses": []map[string]any{
		{"context": "ci/x", "state": "failure", "target_url": "ux"},
	}}
	want := "Checks for #4 at abc1234\nsuccess\ttest\tur\nin_progress\tlint\tul\nfailure\tci/x\tux\n"
	if got := p.must(gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRChecks, Number: 4})).Output; got != want {
		t.Errorf("checks %q", got)
	}

	f.answers["GET /repos/o/r/pulls/4"] = map[string]any{"number": 4, "head": map[string]any{"sha": "../../x"}}
	p.refused(gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRChecks, Number: 4}), "unusable head commit")

	f.answers["POST /repos/o/r/issues/4/comments"] = map[string]any{"html_url": "uc"}
	if got := p.must(gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRComment, Number: 4, Body: "hi"})).Output; got != "Commented on #4: uc\n" {
		t.Errorf("comment %q", got)
	}
	// Someone else's pull request gets a real verdict, and the label.
	f.answers["GET /repos/o/r/pulls/4"] = map[string]any{"number": 4, "user": map[string]any{"login": "someone"}}
	f.answers["GET /user"] = map[string]any{"login": "theuser", "id": 42}
	f.answers["POST /repos/o/r/pulls/4/reviews"] = map[string]any{"html_url": "ur"}
	f.answers["POST /repos/o/r/labels"] = map[string]any{}
	f.answers["POST /repos/o/r/issues/4/labels"] = []any{}
	got := p.must(gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRReview, Number: 4, Review: protocol.ReviewRequestChanges, Body: "no"})).Output
	if got != "Reviewed #4 (request changes): ur\nLabeled #4 gnat:changes-requested\n" {
		t.Errorf("review %q", got)
	}
	asked := f.asked()
	review := asked[len(asked)-3]
	if review.path != "/repos/o/r/pulls/4/reviews" || review.body["event"] != "REQUEST_CHANGES" || review.body["body"] != "no" {
		t.Errorf("review %+v", review)
	}
	p.must(gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRReview, Number: 4, Review: protocol.ReviewApprove}))
	asked = f.asked()
	if review := asked[len(asked)-3]; review.body["event"] != "APPROVE" {
		t.Errorf("review body %v", review.body)
	}

	// A comment is no verdict: no label, and no need to look at the pull request.
	before := len(f.asked())
	p.must(gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRReview, Number: 4, Review: protocol.ReviewComment, Body: "hm"}))
	if asked := f.asked()[before:]; len(asked) != 1 || asked[0].body["event"] != "COMMENT" {
		t.Errorf("asked %+v", asked)
	}
}

func TestGitHubReviewsTheOperatorsOwnPullRequestsWithACommentAndALabel(t *testing.T) {
	f := newFakeGitHub(t)
	f.answers["GET /repos/o/r/pulls/4"] = map[string]any{"number": 4, "user": map[string]any{"login": "TheUser"},
		"labels": []map[string]any{{"name": "bug"}, {"name": "gnat:changes-requested"}}}
	f.answers["GET /user"] = map[string]any{"login": "theuser", "id": 42}
	f.answers["POST /repos/o/r/pulls/4/reviews"] = map[string]any{"html_url": "ur"}
	f.answers["POST /repos/o/r/labels"] = map[string]any{}
	f.answers["POST /repos/o/r/issues/4/labels"] = []any{}
	f.answers["DELETE /repos/o/r/issues/4/labels/gnat:changes-requested"] = []any{}
	p := githubPlane(t, f)
	got := p.must(gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRReview, Number: 4, Review: protocol.ReviewApprove, Body: "Looks right."})).Output
	want := "Reviewed #4 (approve, as a comment, since the pull request's author is the account reviewing it): ur\nLabeled #4 gnat:approved\n"
	if got != want {
		t.Errorf("got %q", got)
	}
	var did []string
	for _, r := range f.asked() {
		did = append(did, r.method+" "+r.path)
	}
	if want := []string{"GET /repos/o/r/pulls/4", "GET /user", "POST /repos/o/r/pulls/4/reviews", "POST /repos/o/r/labels",
		"POST /repos/o/r/issues/4/labels", "DELETE /repos/o/r/issues/4/labels/gnat:changes-requested"}; !reflect.DeepEqual(did, want) {
		t.Fatalf("asked %v", did)
	}
	asked := f.asked()
	if b := asked[2].body; b["event"] != "COMMENT" || b["body"] != "**Approved.**\n\nLooks right." {
		t.Errorf("review %v", b)
	}
	if b := asked[3].body; b["name"] != "gnat:approved" || b["color"] == "" {
		t.Errorf("label made %v", b)
	}
	if b := asked[4].body; !reflect.DeepEqual(b["labels"], []any{"gnat:approved"}) {
		t.Errorf("labels added %v", b)
	}

	// A label it already has isn't added again, and one the repository can't give is
	// reported without undoing the review.
	f.answers["GET /repos/o/r/pulls/4"] = map[string]any{"number": 4, "user": map[string]any{"login": "theuser"}}
	delete(f.answers, "POST /repos/o/r/issues/4/labels")
	got = p.must(gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRReview, Number: 4, Review: protocol.ReviewRequestChanges, Body: "No."})).Output
	want = "Reviewed #4 (request changes, as a comment, since the pull request's author is the account reviewing it): ur\n" +
		"Couldn't label #4 gnat:changes-requested: GitHub answered 404 Not Found: Not Found\n"
	if got != want {
		t.Errorf("got %q", got)
	}
	if b := f.asked()[7].body; b["body"] != "**Changes requested.**\n\nNo." {
		t.Errorf("review %v", b)
	}
	before := len(f.asked())
	f.answers["GET /repos/o/r/pulls/4"] = map[string]any{"number": 4, "user": map[string]any{"login": "theuser"},
		"labels": []map[string]any{{"name": "gnat:approved"}}}
	p.must(gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRReview, Number: 4, Review: protocol.ReviewApprove}))
	for _, r := range f.asked()[before:] {
		if strings.Contains(r.path, "labels") {
			t.Errorf("asked %s %s", r.method, r.path)
		}
	}
}

func TestGitHubErrorsNameWhatGitHubSaid(t *testing.T) {
	f := newFakeGitHub(t)
	f.status["POST /repos/o/r/pulls"] = http.StatusUnprocessableEntity
	f.answers["POST /repos/o/r/pulls"] = map[string]any{"message": "Validation Failed", "errors": []any{map[string]any{"message": "No commits between main and x"}}}
	p := githubPlane(t, f)
	p.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRCreate, Title: "t", Head: "demo/alpha/x", Base: "main"}),
		`422 Unprocessable Entity: Validation Failed ({"message":"No commits between main and x"})`)
}

func TestGitHubNeverEchoesTheToken(t *testing.T) {
	f := newFakeGitHub(t)
	p := githubPlane(t, f)
	for _, a := range []protocol.GitHubRequest{{Action: protocol.PRList}, {Action: protocol.PRView, Number: 9}} {
		_, err := p.request(gh("alpha-secret", a))
		if err == nil || strings.Contains(err.Error(), fakeToken) {
			t.Errorf("%v", err)
		}
	}
}

func TestGitHubWithoutAToken(t *testing.T) {
	f := newFakeGitHub(t)
	p := startPlane(t, t.TempDir())
	if err := p.control.SetSecrets(map[string]string{"alpha": "s"}); err != nil {
		t.Fatal(err)
	}
	if err := p.control.EnableGitHub(GitHub{Swarm: "demo", Repo: "o/r", API: f.server.URL}); err != nil {
		t.Fatal(err)
	}
	p.refused(gh("s", protocol.GitHubRequest{Action: protocol.PRList}), "no GitHub token")
	if got := f.asked(); len(got) != 0 {
		t.Fatalf("GitHub was asked: %+v", got)
	}
}

// rawLine sends one line as it is and returns the answer, for requests no client would make.
func (p *plane) rawLine(line string) string {
	p.t.Helper()
	conn, err := net.DialTimeout("tcp", p.addr, 5*time.Second)
	if err != nil {
		p.t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		p.t.Fatal(err)
	}
	answer, _ := bufio.NewReader(conn).ReadString('\n')
	return answer
}

func TestAnAgentsSecretNeverReachesTheDatabaseTheLogsEventsOrAnyResponse(t *testing.T) {
	const secret = "alpha-secret"
	f := newFakeGitHub(t)
	f.answers["GET /repos/o/r"] = map[string]any{"default_branch": "main"}
	f.answers["POST /repos/o/r/pulls"] = map[string]any{"number": 7, "html_url": "u"}
	f.status["GET /repos/o/r/pulls/9"] = http.StatusInternalServerError
	f.answers["GET /repos/o/r/pulls/9"] = map[string]any{"message": "boom"}
	p := githubPlane(t, f)
	beta := p.attach("beta")
	_ = beta

	var responses []string
	record := func(resp *protocol.Response, err error) {
		if err != nil {
			responses = append(responses, err.Error())
		}
		if resp != nil {
			data, _ := protocol.Encode(resp)
			responses = append(responses, string(data))
		}
	}
	for _, r := range []protocol.GitHubRequest{
		{Action: protocol.PRCreate, Title: "t", Head: "demo/alpha/x"},          // succeeds
		{Action: protocol.PRCreate, Title: "t", Head: "demo/beta/x"},           // refused
		{Action: protocol.PRMerge, Number: 5},                                  // refused
		{Action: protocol.PRView, Number: 9},                                   // GitHub fails
		{Action: protocol.PRList, State: "bogus"},                              // bad request
		{Action: "nonsense"},                                                   // unknown
		{Action: protocol.PRComment, Number: 1, Body: "mentions " + "nothing"}, // GitHub 404
	} {
		record(p.request(gh(secret, r)))
	}
	// A secret that's wrong, one in the wrong place, and requests that don't parse; none
	// may be echoed back. (What a client sends in a field the answer repeats, such as an
	// agent's name, is its own to see.)
	record(p.request(gh(secret+"x", protocol.GitHubRequest{Action: protocol.PRList})))
	record(p.request(protocol.Request{Op: protocol.OpSend, Agent: "alpha", Text: "hi", Secret: secret}))
	record(p.request(protocol.Request{Op: protocol.OpStatus, Agent: "alpha", Secret: secret}))
	record(p.request(protocol.Request{Op: "unknown", Agent: "alpha", Secret: secret}))
	record(p.request(protocol.Request{Op: protocol.OpGitHub, Secret: secret}))
	responses = append(responses,
		p.rawLine(`{"op":"github","secret":"`+secret+`","github":"not an object"}`),
		p.rawLine(`{"op":"github","secret":"`+secret+`","github":{"action":`),
		p.rawLine(`{"op":"github","secret":["`+secret+`"]}`),
	)
	p.close()

	for _, response := range responses {
		if strings.Contains(response, secret) {
			t.Errorf("a response has the secret: %q", response)
		}
	}
	if len(responses) < 10 {
		t.Fatalf("only %d responses", len(responses))
	}
	// Everything the control plane wrote: the database, the logs, the events.
	entries, err := os.ReadDir(p.dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("%v, %v", entries, err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(p.dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(secret)) {
			t.Errorf("%s has the secret", e.Name())
		}
		for _, other := range []string{"beta-secret", "merger-secret", fakeToken} {
			if bytes.Contains(data, []byte(other)) {
				t.Errorf("%s has %s", e.Name(), other)
			}
		}
	}
	// The token reaches only GitHub.
	for _, response := range responses {
		if strings.Contains(response, fakeToken) {
			t.Errorf("a response has the token: %q", response)
		}
	}
}

func TestGitHubGivesTheAccountToCommitAsOnlyToAgentsWithASecret(t *testing.T) {
	f := newFakeGitHub(t)
	f.answers["GET /user"] = map[string]any{"name": "The User", "login": "theuser", "id": 42}
	p := githubPlane(t, f)
	identity := protocol.GitHubRequest{Action: protocol.GitIdentityAction}
	p.refused(gh("forged", identity), "unauthorized")
	p.refused(gh("", identity), "unauthorized")
	if got := f.asked(); len(got) != 0 {
		t.Fatalf("GitHub was asked: %+v", got)
	}
	resp := p.must(gh("alpha-secret", identity))
	if want := (protocol.GitIdentity{Name: "The User", Email: "42+theuser@users.noreply.github.com"}); resp.Identity == nil || *resp.Identity != want {
		t.Errorf("got %+v", resp.Identity)
	}
	// GitHub is asked once, however many agents start.
	p.must(gh("beta-secret", identity))
	if got := f.asked(); len(got) != 1 || got[0].path != "/user" || got[0].auth != "Bearer "+fakeToken {
		t.Errorf("asked %+v", got)
	}
}

func TestGitHubNamesTheAccountByLoginWithoutAName(t *testing.T) {
	f := newFakeGitHub(t)
	f.answers["GET /user"] = map[string]any{"name": nil, "login": "bot", "id": 7}
	p := githubPlane(t, f)
	resp := p.must(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.GitIdentityAction}))
	if resp.Identity.Name != "bot" || resp.Identity.Email != "7+bot@users.noreply.github.com" {
		t.Errorf("got %+v", resp.Identity)
	}
	f.answers["GET /user"] = "not json"
	q := githubPlane(t, f)
	q.refused(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.GitIdentityAction}), "wasn't usable")
}
