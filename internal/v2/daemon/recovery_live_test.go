package daemon

import (
	"os"
	"testing"

	"github.com/kkestell/gnat/internal/v2/store"
)

func TestLiveConversationRecovery(t *testing.T) {
	if os.Getenv("GNAT_LIVE_TEST") != "1" {
		t.Skip("requires an authenticated local Claude Code")
	}
	team := newLiveTeam(t, store.Settings{Harness: "claude", Model: "sonnet", Effort: "low"})
	team.send("Remember the word ORCHARD for my next message. Reply with ORCHARD and end your turn; do not use tools.")
	first := team.wait("first session saved", func(v store.Snapshot) bool { return settled(v) && len(v.Turns) == 1 })
	oldSession := first.Conversations[0].SessionRef
	if oldSession == "" {
		t.Fatal("missing original session")
	}
	team.restart()
	team.send("Reply with the word I asked you to remember and end your turn; do not use tools.")
	resumed := team.wait("exact session resumed", func(v store.Snapshot) bool { return settled(v) && len(v.Turns) == 2 })
	if resumed.Conversations[0].SessionRef != oldSession {
		t.Fatal("restart replaced the session")
	}
	team.control("pause")
	team.send("Reply with FRESH and end your turn; do not use tools.")
	team.control("reset")
	reset := team.wait("new conversation ready with queued input", func(v store.Snapshot) bool {
		return v.Orchestrator.Lifecycle == "ready" && v.Orchestrator.ConversationID != first.Orchestrator.ConversationID
	})
	if len(reset.Conversations) != 2 || len(reset.Turns) != 2 || reset.Messages[2].State != "queued" || reset.Conversations[0].SessionRef != oldSession {
		t.Fatal("reset lost queue or old history", reset)
	}
	team.control("resume")
	fresh := team.wait("queued input delivered to fresh session", func(v store.Snapshot) bool { return settled(v) && len(v.Turns) == 3 })
	if fresh.Turns[2].ConversationID != reset.Orchestrator.ConversationID || fresh.Conversations[1].SessionRef == "" || fresh.Conversations[1].SessionRef == oldSession {
		t.Fatal("fresh turn used old session", fresh)
	}
}
