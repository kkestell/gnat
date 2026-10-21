package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kkestell/gnat/internal/layout"
)

// workspacePath is where the repository is cloned; a variable so tests can use their own.
var workspacePath = layout.ContainerWorkspace

// runCommand runs a command and returns its output; a variable so tests can fake it.
var runCommand = func(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("`%s` failed: %s", strings.Join(append([]string{name}, args...), " "),
			strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// clone clones repo into the workspace, set up to commit and push as the token's GitHub user.
//
// A clone already there, from a restart or an earlier `gnat up`, is left as it was.
func clone(repo string) error {
	out, err := runCommand("gh", "api", "user")
	if err != nil {
		return err
	}
	var user struct {
		Name  string `json:"name"`
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &user); err != nil {
		return fmt.Errorf("can't read the GitHub user: %w", err)
	}
	name := user.Name
	if name == "" {
		name = user.Login
	}
	for _, command := range [][]string{
		{"git", "config", "--global", "user.name", name},
		{"git", "config", "--global", "user.email", fmt.Sprintf("%d+%s@users.noreply.github.com", user.ID, user.Login)},
		// Let git authenticate to GitHub with the same token.
		{"gh", "auth", "setup-git"},
	} {
		if _, err := runCommand(command[0], command[1:]...); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(workspacePath, ".git")); err == nil {
		return nil
	}
	_, err = runCommand("gh", "repo", "clone", repo, workspacePath)
	return err
}

// stagedFile is a file `gnat up` staged outside the workspace, and where it goes in it.
type stagedFile struct{ staged, path string }

// installFiles puts each staged file in the clone, hidden from git.
//
// Each staged file replaces the one in the clone, which outlives the container, and is
// then removed, so a daemon that restarts in the same container leaves the agent's copy
// as it is and installs only the files it didn't get to. A path the repository tracks is
// an error, since git would show the copy as a change.
func installFiles(files []stagedFile) error {
	root, err := resolve(workspacePath)
	if err != nil {
		return err
	}
	exclude := filepath.Join(root, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return err
	}
	var listed []string
	if data, err := os.ReadFile(exclude); err == nil {
		listed = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	for _, f := range files {
		target, err := resolve(filepath.Join(root, f.path))
		if err != nil {
			return err
		}
		if rel, err := filepath.Rel(root, target); err != nil || rel == "." || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%s is outside the workspace", f.path)
		}
		pattern := "/" + excludeEscaper.Replace(f.path)
		if !contains(listed, pattern) {
			if err := appendLine(exclude, pattern); err != nil {
				return err
			}
			listed = append(listed, pattern)
		}
		check := exec.Command("git", "-C", root, "ls-files", "--error-unmatch", f.path)
		if check.Run() == nil {
			return fmt.Errorf("%s is tracked by the repository; it can't be a gnat file", f.path)
		}
		data, err := os.ReadFile(f.staged)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return err
		}
		if err := os.Chmod(target, 0o600); err != nil {
			return err
		}
		if err := os.Remove(f.staged); err != nil {
			return err
		}
	}
	return nil
}

// excludeEscaper escapes what a gitignore pattern would otherwise read as syntax.
var excludeEscaper = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)

func contains(lines []string, line string) bool {
	for _, l := range lines {
		if l == line {
			return true
		}
	}
	return false
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// resolve is the absolute path with its symlinks resolved, in as much of it as exists.
func resolve(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(path)
	if err == nil {
		return real, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent, err := resolve(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}
