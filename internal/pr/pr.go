// Package pr is `gnat-container pr`, the commands an agent works with pull requests by.
// Agents hold no GitHub credential: each command is a request to the control plane, which
// checks it against the rules for this agent and carries it out (see control.GitHub).
// The agent's secret, in its environment, is what says who it is.
package pr

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/kkestell/gnat/internal/protocol"
	"github.com/kkestell/gnat/internal/state"
)

const usage = `usage: gnat-container pr COMMAND [ARGS...]

commands:
  create --title TITLE [--body TEXT | --body-file FILE] [--head BRANCH] [--base BRANCH] [--draft]
  list [--state open|closed|merged|all] [--limit N]
  view NUMBER
  diff NUMBER
  checks NUMBER
  comment NUMBER (--body TEXT | --body-file FILE)
  review NUMBER (--approve | --request-changes | --comment) [--body TEXT | --body-file FILE]
  merge NUMBER [--merge | --squash | --rebase]   (only for agents allowed to merge)
  close NUMBER
  edit NUMBER [--title TITLE] [--body TEXT | --body-file FILE]
  ready NUMBER   (mark a draft ready for review)

close, edit, and ready work only on pull requests from your own branches.
A body of "-" for --body-file is read from stdin.`

// call makes a request to the control plane; a variable so tests can fake it.
var call = func(req protocol.Request) (*protocol.Response, error) {
	return protocol.Call(state.Control, req, 0)
}

// currentBranch is the branch checked out in the working directory.
var currentBranch = func() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	return strings.TrimSpace(string(out)), err
}

// Main is `gnat-container pr`; args are those after `pr`. It returns an exit status: 2 for
// a usage error, 1 for any other failure.
func Main(args []string) int {
	return run(args, os.Getenv(state.SecretVar), os.Stdin, os.Stdout, os.Stderr)
}

// A usageError is arguments that can't be used.
type usageError string

func (e usageError) Error() string { return string(e) }

func run(args []string, secret string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	name, args := args[0], args[1:]
	if name == "-h" || name == "--help" || name == "help" {
		fmt.Fprintln(stdout, usage)
		return 0
	}
	fs := flag.NewFlagSet("gnat-container pr "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		title, body, bodyFile, head, base, status string
		draft, approve, changes, comment          bool
		merge, squash, rebase                     bool
		limit                                     int
	)
	needsNumber := true
	switch name {
	case "create":
		needsNumber = false
		fs.StringVar(&title, "title", "", "the pull request's title")
		fs.StringVar(&head, "head", "", "the branch to open it from (default: the current branch)")
		fs.StringVar(&base, "base", "", "the branch to merge into (default: the repository's default branch)")
		fs.BoolVar(&draft, "draft", false, "open it as a draft")
		fs.StringVar(&body, "body", "", "its description")
		fs.StringVar(&bodyFile, "body-file", "", "read its description from `FILE` (- for stdin)")
	case "list":
		needsNumber = false
		fs.StringVar(&status, "state", "open", "open, closed, merged, or all")
		fs.IntVar(&limit, "limit", 30, "the most pull requests to list")
	case "view", "diff", "checks", "close", "ready":
	case "comment":
		fs.StringVar(&body, "body", "", "the comment")
		fs.StringVar(&bodyFile, "body-file", "", "read the comment from `FILE` (- for stdin)")
	case "review":
		fs.BoolVar(&approve, "approve", false, "approve it")
		fs.BoolVar(&changes, "request-changes", false, "ask for changes")
		fs.BoolVar(&comment, "comment", false, "comment without approving or asking for changes")
		fs.StringVar(&body, "body", "", "the review's text")
		fs.StringVar(&bodyFile, "body-file", "", "read the review's text from `FILE` (- for stdin)")
	case "merge":
		fs.BoolVar(&merge, "merge", false, "merge with a merge commit (the default)")
		fs.BoolVar(&squash, "squash", false, "squash and merge")
		fs.BoolVar(&rebase, "rebase", false, "rebase and merge")
	case "edit":
		fs.StringVar(&title, "title", "", "its new title")
		fs.StringVar(&body, "body", "", "its new description")
		fs.StringVar(&bodyFile, "body-file", "", "read its new description from `FILE` (- for stdin)")
	default:
		fmt.Fprintf(stderr, "gnat-container pr: unknown command '%s'\n%s\n", name, usage)
		return 2
	}
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: gnat-container pr %s\n", name)
		fs.PrintDefaults()
	}
	positional, err := parseInterleaved(fs, args)
	if err == flag.ErrHelp {
		return 0
	}
	if err != nil {
		return 2
	}

	req, err := request(name, positional, needsNumber, func() (string, error) {
		text, err := body, error(nil)
		switch {
		case flagSet(fs, "body") && flagSet(fs, "body-file"):
			return "", usageError("use --body or --body-file, not both")
		case bodyFile == "-":
			var data []byte
			data, err = io.ReadAll(stdin)
			text = string(data)
		case bodyFile != "":
			var data []byte
			data, err = os.ReadFile(bodyFile)
			text = string(data)
		}
		// An edit leaves an empty body as it is, so one given empty can't clear it.
		given := flagSet(fs, "body") || flagSet(fs, "body-file")
		if err == nil && name == "edit" && given && strings.TrimSpace(text) == "" {
			err = usageError("the description can't be empty")
		}
		return text, err
	}, func(r *protocol.GitHubRequest) error {
		switch name {
		case "create":
			r.Title, r.Head, r.Base, r.Draft = title, head, base, draft
			if r.Title == "" {
				return usageError("--title is required")
			}
			if r.Head == "" {
				branch, err := currentBranch()
				if err != nil || branch == "" || branch == "HEAD" {
					return usageError("--head is required: no branch is checked out")
				}
				r.Head = branch
			}
		case "list":
			r.State, r.Limit = status, limit
		case "edit":
			r.Title = title
			switch {
			case !flagSet(fs, "title") && !flagSet(fs, "body") && !flagSet(fs, "body-file"):
				return usageError("give --title, --body, or --body-file")
			case flagSet(fs, "title") && strings.TrimSpace(title) == "":
				return usageError("--title can't be empty")
			}
		case "review":
			switch {
			case count(approve, changes, comment) != 1:
				return usageError("give one of --approve, --request-changes, or --comment")
			case approve:
				r.Review = protocol.ReviewApprove
			case changes:
				r.Review = protocol.ReviewRequestChanges
			default:
				r.Review = protocol.ReviewComment
			}
		case "merge":
			switch {
			case count(merge, squash, rebase) > 1:
				return usageError("give at most one of --merge, --squash, or --rebase")
			case squash:
				r.Method = protocol.MergeSquash
			case rebase:
				r.Method = protocol.MergeRebase
			case merge:
				r.Method = protocol.MergeMerge
			}
		}
		return nil
	})
	if err != nil {
		if u, ok := err.(usageError); ok {
			fmt.Fprintf(stderr, "gnat-container pr %s: %s\n", name, u)
			return 2
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if secret == "" {
		fmt.Fprintf(stderr, "error: %s is not set; this only works inside an agent's container\n", state.SecretVar)
		return 1
	}
	req.Secret = secret
	resp, err := call(req)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	io.WriteString(stdout, resp.Output)
	return 0
}

// actions is each command's action.
var actions = map[string]string{
	"create": protocol.PRCreate, "list": protocol.PRList, "view": protocol.PRView, "diff": protocol.PRDiff,
	"checks": protocol.PRChecks, "comment": protocol.PRComment, "review": protocol.PRReview, "merge": protocol.PRMerge,
	"close": protocol.PRClose, "edit": protocol.PREdit, "ready": protocol.PRReady,
}

// request is the request for a command: its pull request number if it takes one, then
// what fill sets from the flags, and the text, which is read only for the commands that
// have it.
func request(name string, positional []string, needsNumber bool, text func() (string, error),
	fill func(*protocol.GitHubRequest) error) (protocol.Request, error) {
	r := &protocol.GitHubRequest{Action: actions[name]}
	switch {
	case needsNumber && len(positional) != 1:
		return protocol.Request{}, usageError("expected a pull request number")
	case !needsNumber && len(positional) != 0:
		return protocol.Request{}, usageError("unexpected argument '" + positional[0] + "'")
	case needsNumber:
		n, err := strconv.Atoi(strings.TrimPrefix(positional[0], "#"))
		if err != nil || n < 1 {
			return protocol.Request{}, usageError("'" + positional[0] + "' isn't a pull request number")
		}
		r.Number = n
	}
	if err := fill(r); err != nil {
		return protocol.Request{}, err
	}
	switch name {
	case "create", "comment", "review", "edit":
		body, err := text()
		if err != nil {
			return protocol.Request{}, err
		}
		r.Body = body
		if name == "comment" && strings.TrimSpace(body) == "" {
			return protocol.Request{}, usageError("give the comment with --body or --body-file")
		}
	}
	return protocol.Request{Op: protocol.OpGitHub, GitHub: r}, nil
}

func count(flags ...bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}

func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

// parseInterleaved parses args with fs like argparse does: flags may come after the
// positional arguments. It returns the positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}
