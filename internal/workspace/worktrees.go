package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func Git(ctx context.Context, root string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}
func ResolveBase(ctx context.Context, root, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	return Git(ctx, root, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
}
func BranchExists(ctx context.Context, root, branch string) (bool, error) {
	out, err := Git(ctx, root, "for-each-ref", "--format=%(refname)", "refs/heads/"+branch)
	return out != "", err
}

// Reserved paths and branches must be absent before allocation begins.
func CheckAllocation(ctx context.Context, root, path, branch string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("worker path already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	exists, err := BranchExists(ctx, root, branch)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("worker branch already exists: %s", branch)
	}
	return nil
}
func EnsureBranch(ctx context.Context, root, branch, base string) error {
	exists, err := BranchExists(ctx, root, branch)
	if err != nil {
		return err
	}
	if exists {
		actual, err := ResolveBase(ctx, root, "refs/heads/"+branch)
		if err != nil {
			return err
		}
		if actual != base {
			return fmt.Errorf("reserved branch changed externally: %s", branch)
		}
		return nil
	}
	_, err = Git(ctx, root, "branch", branch, base)
	return err
}
func registered(ctx context.Context, root, path string) (string, bool, error) {
	out, err := Git(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", false, err
	}
	current := ""
	branch := ""
	for _, field := range strings.Split(out, "\x00") {
		if strings.HasPrefix(field, "worktree ") {
			current = strings.TrimPrefix(field, "worktree ")
			branch = ""
		}
		if strings.HasPrefix(field, "branch ") {
			branch = strings.TrimPrefix(field, "branch refs/heads/")
		}
		if field == "" && current == path {
			return branch, true, nil
		}
	}
	return "", false, nil
}
func EnsureWorktree(ctx context.Context, root, path, branch string) error {
	actual, found, err := registered(ctx, root, path)
	if err != nil {
		return err
	}
	if found {
		if actual != branch {
			return fmt.Errorf("registered worker branch changed: expected %s, found %s", branch, actual)
		}
		w, err := Discover(path)
		if err != nil {
			return err
		}
		original, err := Discover(root)
		if err != nil {
			return err
		}
		if w.Root != path || w.CommonDir != original.CommonDir {
			return fmt.Errorf("worker checkout ownership differs: %s", path)
		}
		return nil
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("refusing unregistered worker path: %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	_, err = Git(ctx, root, "worktree", "add", path, branch)
	return err
}
func RemoveWorktree(ctx context.Context, root, path, branch string) error {
	actual, found, err := registered(ctx, root, path)
	if err != nil {
		return err
	}
	if !found {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("refusing unregistered worker path: %s", path)
	}
	if actual != branch {
		return fmt.Errorf("worker branch changed: expected %s, found %s", branch, actual)
	}
	if _, err = Git(ctx, root, "worktree", "remove", path); err != nil {
		return err
	}
	return nil
}
func RemoveBranch(ctx context.Context, root, branch string) error {
	exists, err := BranchExists(ctx, root, branch)
	if err != nil || !exists {
		return err
	}
	_, err = Git(ctx, root, "branch", "-d", branch)
	return err
}
