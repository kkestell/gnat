package control

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
	err := p.control.EnableGitHub(GitHub{
		Swarm: "demo", Repo: "o/r", Token: fakeToken, API: f.server.URL,
		Secrets: map[string]string{"alpha": "alpha-secret", "beta": "beta-secret", "merger": "merger-secret"},
	})
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
}

func TestEnableGitHubChecksItsArguments(t *testing.T) {
	p := startPlane(t, t.TempDir())
	for _, repo := range []string{"", "r", "o/r/x", "o/r?x=1", "o/../r", "o/r\n", "../..", "./r", "o/.."} {
		if err := p.control.EnableGitHub(GitHub{Swarm: "demo", Repo: repo}); err == nil {
			t.Errorf("accepted repository %q", repo)
		}
	}
	if err := p.control.EnableGitHub(GitHub{Swarm: "demo", Repo: "o/r", Secrets: map[string]string{"ghost": "x"}}); err == nil {
		t.Error("accepted a secret for an agent that isn't in the swarm")
	}
	// An agent whose secret is empty has none.
	if err := p.control.EnableGitHub(GitHub{Swarm: "demo", Repo: "o/r", Secrets: map[string]string{"alpha": ""}}); err != nil {
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
	if err := p.control.EnableGitHub(GitHub{Swarm: "demo", Repo: "o/r", Token: fakeToken, API: f.server.URL,
		Secrets: map[string]string{"a..b": "s1", "x.lock": "s2", "ok": "s3", "dot.": "s4"}}); err != nil {
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
	if err := q.control.EnableGitHub(GitHub{Swarm: "my..swarm", Repo: "o/r", Token: fakeToken, API: f.server.URL,
		Secrets: map[string]string{"ok": "s"}}); err != nil {
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
	p := githubPlane(t, f)
	for _, a := range []protocol.GitHubRequest{
		{Action: protocol.PRList}, {Action: protocol.PRView, Number: 1}, {Action: protocol.PRDiff, Number: 1},
		{Action: protocol.PRChecks, Number: 1}, {Action: protocol.PRComment, Number: 1, Body: "x"},
		{Action: protocol.PRReview, Number: 1, Review: protocol.ReviewApprove},
		{Action: protocol.PRCreate, Title: "t", Head: "demo/merger/x", Base: "main"}, {Action: protocol.PRMerge, Number: 1},
	} {
		_, _ = p.request(gh("merger-secret", a)) // GitHub answers 404; only where it was asked matters.
	}
	got := f.asked()
	if len(got) < 8 {
		t.Fatalf("asked %+v", got)
	}
	for _, r := range got {
		if !strings.HasPrefix(r.path, "/repos/o/r/") {
			t.Errorf("%s %s", r.method, r.path)
		}
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
			"head": map[string]any{"ref": "demo/beta/b"}, "base": map[string]any{"ref": "main"}, "user": map[string]any{"login": "kyle"}},
		{"number": 1, "title": "Closed one", "html_url": "u1", "state": "closed",
			"head": map[string]any{"ref": "c"}, "base": map[string]any{"ref": "main"}, "user": map[string]any{"login": "kyle"}},
	}
	p := githubPlane(t, f)
	all := p.must(gh("alpha-secret", protocol.GitHubRequest{Action: protocol.PRList, State: "all"})).Output
	want := "#3\topen\tOpen one\tdemo/alpha/a → main\tkyle (u3, draft)\n" +
		"#2\tmerged\tMerged one\tdemo/beta/b → main\tkyle (u2)\n" +
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
		"user": map[string]any{"login": "kyle"},
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
	f.answers["POST /repos/o/r/pulls/4/reviews"] = map[string]any{"html_url": "ur"}
	got := p.must(gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRReview, Number: 4, Review: protocol.ReviewRequestChanges, Body: "no"})).Output
	if got != "Reviewed #4 (request changes): ur\n" {
		t.Errorf("review %q", got)
	}
	last := f.asked()[len(f.asked())-1]
	if last.body["event"] != "REQUEST_CHANGES" || last.body["body"] != "no" {
		t.Errorf("review body %v", last.body)
	}
	p.must(gh("beta-secret", protocol.GitHubRequest{Action: protocol.PRReview, Number: 4, Review: protocol.ReviewApprove}))
	if last := f.asked()[len(f.asked())-1]; last.body["event"] != "APPROVE" {
		t.Errorf("review body %v", last.body)
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
	if err := p.control.EnableGitHub(GitHub{Swarm: "demo", Repo: "o/r", API: f.server.URL, Secrets: map[string]string{"alpha": "s"}}); err != nil {
		t.Fatal(err)
	}
	p.refused(gh("s", protocol.GitHubRequest{Action: protocol.PRList}), "no GitHub token")
	if got := f.asked(); len(got) != 0 {
		t.Fatalf("GitHub was asked: %+v", got)
	}
}
