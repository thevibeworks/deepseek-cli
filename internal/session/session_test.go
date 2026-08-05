package session

import (
	"testing"

	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("DEEPSEEK_STATE_DIR", t.TempDir())
}

func conversation() *Session {
	return &Session{
		Name: "t",
		Messages: []deepseek.Message{
			{Role: "user", Content: "q1"},
			{Role: "assistant", Content: "a1", ReasoningContent: "cot1"},
			{Role: "user", Content: "q2"},
			{Role: "assistant", Content: "a2", ReasoningContent: "cot2"},
		},
	}
}

// The chain-of-thought replay rule is the sharpest edge in the DeepSeek
// API: with tools in the request, every assistant reasoning_content must
// be sent back or the API answers 400. Without tools it is ignored
// server-side, so replaying it just burns input tokens.

func TestHistoryStripsReasoningWithoutTools(t *testing.T) {
	got := conversation().History(false)
	for _, m := range got {
		if m.Role == "assistant" && m.ReasoningContent != "" {
			t.Fatalf("reasoning_content should be dropped when no tools are sent: %+v", m)
		}
	}
	if len(got) != 4 {
		t.Errorf("dropped messages, got %d want 4", len(got))
	}
}

func TestHistoryReplaysReasoningWithTools(t *testing.T) {
	got := conversation().History(true)
	var seen int
	for _, m := range got {
		if m.Role == "assistant" {
			if m.ReasoningContent == "" {
				t.Fatalf("reasoning_content must be replayed with tools: %+v", m)
			}
			seen++
		}
	}
	if seen != 2 {
		t.Errorf("checked %d assistant messages, want 2", seen)
	}
}

func TestHistoryKeepsReplayingOnceToolsWereUsed(t *testing.T) {
	// A conversation that used tools earlier must keep replaying its whole
	// chain of thought, even on a later turn that carries no tools —
	// DeepSeek requires it for the conversation, not just for the turn.
	s := conversation()
	s.UsedTools = true

	for _, m := range s.History(false) {
		if m.Role == "assistant" && m.ReasoningContent == "" {
			t.Fatalf("reasoning dropped on a tool-using conversation: %+v", m)
		}
	}
}

func TestHistoryDoesNotMutateStoredMessages(t *testing.T) {
	// Stripping is for the wire only. If it edited the stored session, a
	// conversation could never start replaying its history later.
	s := conversation()
	_ = s.History(false)

	if s.Messages[1].ReasoningContent != "cot1" {
		t.Errorf("History mutated the stored session: %+v", s.Messages[1])
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	isolate(t)

	s := conversation()
	s.Name = "work"
	s.Model = deepseek.ModelPro
	s.UsedTools = true
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	got, err := Load("work")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 4 || got.Model != deepseek.ModelPro || !got.UsedTools {
		t.Errorf("round trip lost data: %+v", got)
	}
	if got.Messages[1].ReasoningContent != "cot1" {
		t.Errorf("reasoning not persisted: %+v", got.Messages[1])
	}
}

func TestLoadMissingSessionIsEmptyNotAnError(t *testing.T) {
	// `chat --session brand-new` has to just work.
	isolate(t)

	s, err := Load("brand-new")
	if err != nil {
		t.Fatalf("a fresh session name should not error: %v", err)
	}
	if !s.Empty() {
		t.Errorf("want an empty session, got %+v", s)
	}
}

func TestRejectsNamesThatEscapeTheStateDir(t *testing.T) {
	isolate(t)

	for _, name := range []string{"../escape", "a/b", `a\b`, "..", ".", ""} {
		if _, err := Load(name); err == nil {
			t.Errorf("Load(%q) should be rejected — a session name is a filename", name)
		}
	}
}

func TestListSortsMostRecentFirst(t *testing.T) {
	isolate(t)

	for _, name := range []string{"older", "newer"} {
		s := &Session{Name: name, Messages: []deepseek.Message{{Role: "user", Content: "x"}}}
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
	}

	metas, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Fatalf("listed %d sessions, want 2", len(metas))
	}
	if metas[0].Name != "newer" {
		t.Errorf("most recent should sort first, got %q", metas[0].Name)
	}
}

func TestListOnEmptyStateDir(t *testing.T) {
	isolate(t)

	metas, err := List()
	if err != nil {
		t.Fatalf("no sessions yet is not an error: %v", err)
	}
	if len(metas) != 0 {
		t.Errorf("got %d sessions", len(metas))
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	isolate(t)

	s := &Session{Name: "gone", Messages: []deepseek.Message{{Role: "user", Content: "x"}}}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if err := Remove("gone"); err != nil {
		t.Fatal(err)
	}
	// The desired end state already holds; that is not a failure.
	if err := Remove("gone"); err != nil {
		t.Errorf("removing a missing session should succeed, got %v", err)
	}
}
