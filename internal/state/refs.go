package state

import "strings"

// BranchPrefix is what every branch an agent may push or open a pull request from starts
// with: SWARM/AGENT/. It fails for a name that would make an invalid git ref, which
// NamePattern allows, such as `a..b` or `x.lock`.
func BranchPrefix(swarm, agent string) (string, error) {
	if !ValidBranch(swarm + "/" + agent + "/x") {
		return "", errorf("swarm '%s' with agent '%s' can't name branches: a name can't contain '..', "+
			"end in '.' or '.lock', or otherwise make an invalid git ref", swarm, agent)
	}
	return swarm + "/" + agent + "/", nil
}

// ValidBranch is whether name is a branch name git accepts (see git-check-ref-format)
// and one that can't be taken for an option or a "fork:branch" head. It is a little
// stricter than git: no component may end in ".".
func ValidBranch(name string) bool {
	if name == "" || name == "@" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") ||
		strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.Contains(name, "//") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return false
		}
	}
	for _, component := range strings.Split(name, "/") {
		if strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}
