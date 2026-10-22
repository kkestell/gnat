package state

import "testing"

func TestBranchPrefix(t *testing.T) {
	got, err := BranchPrefix("demo", "alpha-1")
	if err != nil || got != "demo/alpha-1/" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, names := range [][2]string{
		{"demo", "a..b"}, {"a..b", "alpha"}, {"demo", "x.lock"}, {"x.lock", "alpha"},
		{"demo", "alpha."}, {"demo", "a.lock.b.lock"},
	} {
		if p, err := BranchPrefix(names[0], names[1]); err == nil {
			t.Errorf("%v gave prefix %q", names, p)
		}
	}
	// Names NamePattern allows that make valid refs.
	for _, names := range [][2]string{{"demo", "a.b"}, {"demo", "lock"}, {"v1.2", "x_y-z"}} {
		if _, err := BranchPrefix(names[0], names[1]); err != nil {
			t.Errorf("%v: %v", names, err)
		}
	}
}

func TestValidBranch(t *testing.T) {
	for _, ok := range []string{"main", "demo/alpha/fix-1", "a/b.c/d", "x@y"} {
		if !ValidBranch(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{
		"", "@", "-x", "/x", "x/", "a//b", "a..b", "a@{b", "a b", "a:b", "owner:branch", "a~b", "a^b", "a?b", "a*b",
		"a[b", "a\\b", "a\x00b", "a\nb", "a.", ".a", "a/.b", "a.lock", "a.lock/b",
	} {
		if ValidBranch(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
