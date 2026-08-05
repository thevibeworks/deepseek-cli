package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
	"github.com/thevibeworks/deepseek-cli/internal/session"
)

// The slash commands are the interactive surface, and every one of them
// mutates state a later turn depends on. They are plain functions, so
// they are tested as such — the terminal handling around them is not.

func newTestTurn(t *testing.T) (*chatTurn, *bytes.Buffer) {
	t.Helper()
	t.Setenv("DEEPSEEK_STATE_DIR", t.TempDir())

	var stderr bytes.Buffer
	o := &Options{stdout: &bytes.Buffer{}, stderr: &stderr}
	f := &chatFlags{model: deepseek.ModelFlash}
	cmd := newChatCmd(o)
	return &chatTurn{
		o: o, f: f, cmd: cmd,
		sess:     &session.Session{Name: "test"},
		sessName: "test",
	}, &stderr
}

func TestREPLCommandsMutateState(t *testing.T) {
	ctx := context.Background()

	t.Run("model switches on a short name", func(t *testing.T) {
		turn, out := newTestTurn(t)
		if done, err := turn.command(ctx, "/model pro"); done || err != nil {
			t.Fatalf("/model pro = %v, %v", done, err)
		}
		if turn.f.model != deepseek.ModelPro {
			t.Errorf("model = %q, want pro", turn.f.model)
		}
		if !strings.Contains(out.String(), deepseek.ModelPro) {
			t.Error("the switch was not confirmed on stderr")
		}
	})

	t.Run("model rejects nonsense without changing anything", func(t *testing.T) {
		turn, _ := newTestTurn(t)
		if _, err := turn.command(ctx, "/model gpt-4"); err == nil {
			t.Error("want an error for an unknown model")
		}
		if turn.f.model != deepseek.ModelFlash {
			t.Errorf("a rejected /model changed the model to %q", turn.f.model)
		}
	})

	t.Run("think", func(t *testing.T) {
		turn, _ := newTestTurn(t)
		if _, err := turn.command(ctx, "/think off"); err != nil {
			t.Fatalf("/think off: %v", err)
		}
		if turn.f.think != "off" {
			t.Errorf("think = %q", turn.f.think)
		}
		if _, err := turn.command(ctx, "/think sideways"); err == nil {
			t.Error("want an error for a bad value")
		}
	})

	t.Run("effort accepts every level the API takes", func(t *testing.T) {
		turn, _ := newTestTurn(t)
		for _, e := range []string{"none", "minimal", "low", "high", "max"} {
			if _, err := turn.command(ctx, "/effort "+e); err != nil {
				t.Errorf("/effort %s: %v", e, err)
			}
			if turn.f.effort != e {
				t.Errorf("effort = %q, want %q", turn.f.effort, e)
			}
		}
		if _, err := turn.command(ctx, "/effort bogus"); err == nil {
			t.Error("want an error for a level the API rejects")
		}
	})

	t.Run("new clears history but keeps the name", func(t *testing.T) {
		turn, _ := newTestTurn(t)
		turn.sess.Add(deepseek.Message{Role: "user", Content: "hi"})
		if _, err := turn.command(ctx, "/new"); err != nil {
			t.Fatalf("/new: %v", err)
		}
		if !turn.sess.Empty() {
			t.Error("/new left history behind")
		}
		if turn.sess.Name != "test" {
			t.Errorf("/new renamed the session to %q", turn.sess.Name)
		}
	})

	t.Run("system sets the prompt", func(t *testing.T) {
		turn, _ := newTestTurn(t)
		if _, err := turn.command(ctx, "/system be terse"); err != nil {
			t.Fatalf("/system: %v", err)
		}
		if turn.system != "be terse" {
			t.Errorf("system = %q", turn.system)
		}
	})

	t.Run("file attaches to the next turn only", func(t *testing.T) {
		turn, out := newTestTurn(t)
		path := filepath.Join(t.TempDir(), "notes.md")
		if err := os.WriteFile(path, []byte("hello from a file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := turn.command(ctx, "/file "+path); err != nil {
			t.Fatalf("/file: %v", err)
		}
		if len(turn.pending) != 1 || !strings.Contains(turn.pending[0], "hello from a file") {
			t.Errorf("pending = %v", turn.pending)
		}
		if !strings.Contains(out.String(), "attached") {
			t.Error("no confirmation")
		}
		if _, err := turn.command(ctx, "/file /no/such/path"); err == nil {
			t.Error("want an error for a missing file")
		}
	})

	t.Run("save renames and persists", func(t *testing.T) {
		turn, out := newTestTurn(t)
		turn.sess.Add(deepseek.Message{Role: "user", Content: "hi"})
		if _, err := turn.command(ctx, "/save keeper"); err != nil {
			t.Fatalf("/save: %v", err)
		}
		if turn.sessName != "keeper" {
			t.Errorf("sessName = %q", turn.sessName)
		}
		reloaded, err := session.Load("keeper")
		if err != nil {
			t.Fatalf("reloading: %v", err)
		}
		if len(reloaded.Messages) != 1 {
			t.Errorf("saved session has %d messages, want 1", len(reloaded.Messages))
		}
		if !strings.Contains(out.String(), "--session keeper") {
			t.Error("/save should say how to resume")
		}
		if _, err := turn.command(ctx, "/save"); err == nil {
			t.Error("/save with no name should error")
		}
	})

	t.Run("exit ends the loop", func(t *testing.T) {
		turn, _ := newTestTurn(t)
		for _, form := range []string{"/exit", "/quit", "/q"} {
			done, err := turn.command(ctx, form)
			if !done || err != nil {
				t.Errorf("%s = %v, %v; want done", form, done, err)
			}
		}
	})

	t.Run("help lists every command it dispatches", func(t *testing.T) {
		turn, out := newTestTurn(t)
		if done, err := turn.command(ctx, "/help"); done || err != nil {
			t.Fatalf("/help = %v, %v", done, err)
		}
		text := out.String()
		for _, name := range []string{"/new", "/model", "/think", "/effort", "/system", "/file", "/tokens", "/docs", "/session", "/save", "/exit"} {
			if !strings.Contains(text, name) {
				t.Errorf("/help does not mention %s", name)
			}
		}
	})

	t.Run("unknown commands point at help", func(t *testing.T) {
		turn, _ := newTestTurn(t)
		_, err := turn.command(ctx, "/frobnicate")
		if err == nil || !strings.Contains(err.Error(), "/help") {
			t.Errorf("unknown command error = %v, want a pointer to /help", err)
		}
	})

	t.Run("docs searches offline", func(t *testing.T) {
		turn, out := newTestTurn(t)
		if _, err := turn.command(ctx, "/docs context caching"); err != nil {
			t.Fatalf("/docs: %v", err)
		}
		if !strings.Contains(out.String(), "guides/kv_cache") {
			t.Errorf("/docs found nothing useful:\n%s", out.String())
		}
		if _, err := turn.command(ctx, "/docs"); err == nil {
			t.Error("/docs with no query should error")
		}
	})
}

func TestREPLFarewellNamesTheSession(t *testing.T) {
	turn, _ := newTestTurn(t)
	if got := turn.replFarewell(); got != "bye" {
		t.Errorf("an empty conversation says %q, want a plain bye", got)
	}
	turn.sess.Add(deepseek.Message{Role: "user", Content: "hi"})
	got := turn.replFarewell()
	if !strings.Contains(got, "chat -c") || !strings.Contains(got, "test") {
		t.Errorf("farewell = %q; it should name the session and how to resume", got)
	}
}

func TestCheckInteractiveRefusesNonTerminals(t *testing.T) {
	// Tests never run with a terminal on stdin, which is exactly the case
	// this guards.
	o := &Options{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	err := o.checkInteractive()
	if err == nil {
		t.Fatal("want a refusal without a terminal")
	}
	if !strings.Contains(err.Error(), "--session") {
		t.Errorf("the refusal should offer the scripted alternative, got: %v", err)
	}

	o.JSON = true
	if err := o.checkInteractive(); err == nil {
		t.Error("--interactive with --json should be refused")
	}
}

func TestPlural(t *testing.T) {
	if got := plural(1, "byte"); got != "1 byte" {
		t.Errorf("plural(1) = %q", got)
	}
	if got := plural(2, "byte"); got != "2 bytes" {
		t.Errorf("plural(2) = %q", got)
	}
}
