package layout

import (
	"reflect"
	"testing"
)

func TestCommand(t *testing.T) {
	got := Command("send", "alpha", "-m", "hi")
	want := []string{"/opt/gnat/gnat", "internal", "send", "alpha", "-m", "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := Command("hook"); !reflect.DeepEqual(got, []string{BinaryPath, "internal", "hook"}) {
		t.Fatalf("got %v", got)
	}
}
