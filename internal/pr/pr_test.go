package pr

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kkestell/gnat/internal/protocol"
)

// fake answers each request with output, or err, and records them.
func fake(t *testing.T, output string, err error) *[]protocol.Request {
	t.Helper()
	var requests []protocol.Request
	saved, savedBranch := call, currentBranch
	call = func(req protocol.Request) (*protocol.Response, error) {
		requests = append(requests, req)
		if err != nil {
			return nil, err
		}
		r := protocol.OK()
		r.Output = output
		return r, nil
	}
	currentBranch = func() (string, error) { return "demo/alpha/work", nil }
	t.Cleanup(func() { call, currentBranch = saved, savedBranch })
	return &requests
}

func do(args []string, stdin string) (out, errs string, status int) {
	var o, e bytes.Buffer
	status = run(args, "the-secret", strings.NewReader(stdin), &o, &e)
	return o.String(), e.String(), status
}

func req(r protocol.GitHubRequest) protocol.Request {
	return protocol.Request{Op: protocol.OpGitHub, Secret: "the-secret", GitHub: &r}
}

func TestCommandsSendTheirRequestWithTheSecret(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "body.md")
	os.WriteFile(file, []byte("from a file"), 0o644)
	for _, c := range []struct {
		args  []string
		stdin string
		want  protocol.GitHubRequest
	}{
		{[]string{"create", "--title", "T", "--body", "B", "--base", "main", "--draft"}, "",
			protocol.GitHubRequest{Action: protocol.PRCreate, Title: "T", Body: "B", Head: "demo/alpha/work", Base: "main", Draft: true}},
		{[]string{"create", "--head", "demo/alpha/x", "--title", "T", "--body-file", file}, "",
			protocol.GitHubRequest{Action: protocol.PRCreate, Title: "T", Body: "from a file", Head: "demo/alpha/x"}},
		{[]string{"create", "--title", "T", "--body-file", "-"}, "piped",
			protocol.GitHubRequest{Action: protocol.PRCreate, Title: "T", Body: "piped", Head: "demo/alpha/work"}},
		{[]string{"list"}, "", protocol.GitHubRequest{Action: protocol.PRList, State: "open", Limit: 30}},
		{[]string{"list", "--state", "merged", "--limit", "5"}, "", protocol.GitHubRequest{Action: protocol.PRList, State: "merged", Limit: 5}},
		{[]string{"view", "12"}, "", protocol.GitHubRequest{Action: protocol.PRView, Number: 12}},
		{[]string{"view", "#12"}, "", protocol.GitHubRequest{Action: protocol.PRView, Number: 12}},
		{[]string{"diff", "3"}, "", protocol.GitHubRequest{Action: protocol.PRDiff, Number: 3}},
		{[]string{"checks", "3"}, "", protocol.GitHubRequest{Action: protocol.PRChecks, Number: 3}},
		{[]string{"comment", "3", "--body", "hi"}, "", protocol.GitHubRequest{Action: protocol.PRComment, Number: 3, Body: "hi"}},
		{[]string{"comment", "--body", "hi", "3"}, "", protocol.GitHubRequest{Action: protocol.PRComment, Number: 3, Body: "hi"}},
		{[]string{"review", "3", "--approve"}, "", protocol.GitHubRequest{Action: protocol.PRReview, Number: 3, Review: protocol.ReviewApprove}},
		{[]string{"review", "3", "--request-changes", "--body", "no"}, "",
			protocol.GitHubRequest{Action: protocol.PRReview, Number: 3, Review: protocol.ReviewRequestChanges, Body: "no"}},
		{[]string{"review", "3", "--comment", "--body-file", "-"}, "text",
			protocol.GitHubRequest{Action: protocol.PRReview, Number: 3, Review: protocol.ReviewComment, Body: "text"}},
		{[]string{"merge", "3"}, "", protocol.GitHubRequest{Action: protocol.PRMerge, Number: 3}},
		{[]string{"merge", "3", "--squash"}, "", protocol.GitHubRequest{Action: protocol.PRMerge, Number: 3, Method: protocol.MergeSquash}},
		{[]string{"merge", "3", "--rebase"}, "", protocol.GitHubRequest{Action: protocol.PRMerge, Number: 3, Method: protocol.MergeRebase}},
		{[]string{"close", "#3"}, "", protocol.GitHubRequest{Action: protocol.PRClose, Number: 3}},
		{[]string{"edit", "3", "--title", "T"}, "", protocol.GitHubRequest{Action: protocol.PREdit, Number: 3, Title: "T"}},
		{[]string{"edit", "--body", "B", "3"}, "", protocol.GitHubRequest{Action: protocol.PREdit, Number: 3, Body: "B"}},
		{[]string{"edit", "3", "--title", "T", "--body-file", file}, "",
			protocol.GitHubRequest{Action: protocol.PREdit, Number: 3, Title: "T", Body: "from a file"}},
		{[]string{"edit", "3", "--body-file", "-"}, "piped", protocol.GitHubRequest{Action: protocol.PREdit, Number: 3, Body: "piped"}},
		{[]string{"ready", "#3"}, "", protocol.GitHubRequest{Action: protocol.PRReady, Number: 3}},
	} {
		got := fake(t, "the output\n", nil)
		out, errs, status := do(c.args, c.stdin)
		if status != 0 || out != "the output\n" || errs != "" {
			t.Errorf("%v: status %d, out %q, err %q", c.args, status, out, errs)
		}
		if want := []protocol.Request{req(c.want)}; !reflect.DeepEqual(*got, want) {
			t.Errorf("%v: got %+v %+v, want %+v", c.args, (*got)[0], *(*got)[0].GitHub, want[0])
		}
	}
}

func TestUsageErrorsSendNothing(t *testing.T) {
	for _, args := range [][]string{
		nil, {"bogus"}, {"view"}, {"view", "x"}, {"view", "0"}, {"view", "-3"}, {"view", "1", "2"}, {"list", "5"},
		{"create"}, {"create", "--title", "T", "extra"},
		{"comment", "1"}, {"comment", "1", "--body", "  "},
		{"review", "1"}, {"review", "1", "--approve", "--comment"},
		{"merge", "1", "--squash", "--rebase"},
		{"create", "--title", "T", "--body", "a", "--body-file", "x"},
		{"view", "1", "--nope"},
		{"close"}, {"close", "1", "2"}, {"close", "1", "--body", "x"},
		{"ready"}, {"ready", "1", "2"},
		{"edit"}, {"edit", "1"}, {"edit", "1", "--title", " "}, {"edit", "1", "--body", ""},
		{"edit", "1", "--title", "T", "--body", "  "}, {"edit", "1", "--body-file", "-"},
		{"edit", "1", "--body", "a", "--body-file", "x"},
	} {
		got := fake(t, "", nil)
		if _, _, status := do(args, ""); status != 2 {
			t.Errorf("%v: status %d, want 2", args, status)
		}
		if len(*got) != 0 {
			t.Errorf("%v: sent %+v", args, *got)
		}
	}
}

func TestHeadMustBeKnown(t *testing.T) {
	got := fake(t, "", nil)
	currentBranch = func() (string, error) { return "HEAD", nil }
	if _, errs, status := do([]string{"create", "--title", "T"}, ""); status != 2 || !strings.Contains(errs, "--head is required") {
		t.Errorf("status %d, %q", status, errs)
	}
	if len(*got) != 0 {
		t.Errorf("sent %+v", *got)
	}
}

func TestFailuresAreReported(t *testing.T) {
	fake(t, "", errors.New("agent 'alpha' is not allowed to merge pull requests"))
	out, errs, status := do([]string{"merge", "3"}, "")
	if status != 1 || out != "" || errs != "error: agent 'alpha' is not allowed to merge pull requests\n" {
		t.Errorf("status %d, out %q, err %q", status, out, errs)
	}
}

func TestWithoutASecretNothingIsSent(t *testing.T) {
	got := fake(t, "", nil)
	var o, e bytes.Buffer
	if status := run([]string{"list"}, "", strings.NewReader(""), &o, &e); status != 1 || !strings.Contains(e.String(), "GNAT_SECRET is not set") {
		t.Errorf("status %d, %q", status, e.String())
	}
	if len(*got) != 0 {
		t.Errorf("sent %+v", *got)
	}
}

func TestHelp(t *testing.T) {
	fake(t, "", nil)
	if out, _, status := do([]string{"--help"}, ""); status != 0 || !strings.Contains(out, "merge NUMBER") {
		t.Errorf("status %d, %q", status, out)
	}
}
