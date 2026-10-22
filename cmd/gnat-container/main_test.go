package main

import (
	"reflect"
	"testing"
)

func TestSubcommandsGetTheirArgumentsUntouched(t *testing.T) {
	var got []string
	saved := commands["send"]
	commands["send"] = func(args []string) int { got = args; return 3 }
	t.Cleanup(func() { commands["send"] = saved })
	if status := run([]string{"send", "alpha", "-m", "hi", "--now", "--help"}); status != 3 {
		t.Errorf("status %d", status)
	}
	if want := []string{"alpha", "-m", "hi", "--now", "--help"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestUnknownOrMissingSubcommandIsAUsageError(t *testing.T) {
	for _, args := range [][]string{nil, {"internal", "send"}, {"bogus"}} {
		if status := run(args); status != 2 {
			t.Errorf("run(%v) = %d, want 2", args, status)
		}
	}
}
