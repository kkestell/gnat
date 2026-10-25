// Package workspace discovers local checkouts without changing their Git state.
package workspace

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type Workspace struct {
	Root      string `json:"root"`
	CommonDir string `json:"common_dir"`
	Name      string `json:"name"`
}

func Discover(dir string) (Workspace, error) {
	git := func(args ...string) (string, error) {
		output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(output)), err)
		}
		return strings.TrimSpace(string(output)), nil
	}
	bare, err := git("rev-parse", "--is-bare-repository")
	if err != nil {
		return Workspace{}, err
	}
	if bare == "true" {
		return Workspace{}, fmt.Errorf("bare repositories are not workspaces")
	}
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return Workspace{}, err
	}
	if _, err := git("rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return Workspace{}, fmt.Errorf("workspace needs an initial commit: %w", err)
	}
	common, err := git("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Workspace{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Workspace{}, err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return Workspace{}, err
	}
	return Workspace{Root: root, CommonDir: common, Name: filepath.Base(root)}, nil
}
