// Package session persists chat conversations between invocations.
//
// The API is stateless — nothing is stored server-side, in any of the
// four formats — so multi-turn means resending the whole history every
// time. This package owns that history and, with it, the one rule that
// is easy to get wrong and expensive when you do: which assistant
// reasoning_content has to go back on the wire.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

// Default is the session used when none is named.
const Default = "last"

// Session is a stored conversation.
type Session struct {
	Name     string             `json:"name"`
	Model    string             `json:"model,omitempty"`
	Created  time.Time          `json:"created"`
	Updated  time.Time          `json:"updated"`
	Messages []deepseek.Message `json:"messages"`
	// UsedTools records whether any turn in this conversation carried the
	// tools parameter. Once true it stays true: DeepSeek requires the
	// chain of thought to be replayed for the whole conversation, not
	// just for the turns that had tools attached.
	UsedTools bool `json:"used_tools,omitempty"`
}

func dir() string { return filepath.Join(deepseek.StateDir(), "sessions") }

func path(name string) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	return filepath.Join(dir(), name+".json"), nil
}

// validName keeps a session name a single path element. A name is used
// as a filename, so a slash or a .. would write outside the state dir.
func validName(name string) error {
	if name == "" {
		return errors.New("session name is empty")
	}
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("invalid session name %q — use a plain name like work or refactor-auth", name)
	}
	return nil
}

// Load reads a session. A session that does not exist yet loads as an
// empty one, so `chat --session new-thing` just works.
func Load(name string) (*Session, error) {
	p, err := path(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Session{Name: name, Created: time.Now().UTC()}, nil
		}
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("reading session %q: %w", name, err)
	}
	s.Name = name
	return &s, nil
}

// Save writes the session atomically, so an interrupted write cannot
// leave a half-file that fails to parse on the next turn.
func (s *Session) Save() error {
	p, err := path(s.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	s.Updated = time.Now().UTC()
	if s.Created.IsZero() {
		s.Created = s.Updated
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(p), ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Empty reports whether the conversation has no turns yet.
func (s *Session) Empty() bool { return len(s.Messages) == 0 }

// Add appends a message verbatim.
func (s *Session) Add(m deepseek.Message) { s.Messages = append(s.Messages, m) }

// History returns the messages to send for the next request.
//
// This is where DeepSeek's chain-of-thought rule is enforced. When a
// request carries tools, every assistant message's reasoning_content must
// be replayed or the API answers 400. When it does not, reasoning_content
// is ignored server-side, so it is stripped: replaying it would spend
// input tokens on text the model will discard.
//
// The stored session always keeps the reasoning, so a conversation that
// starts without tools and later gains them can still replay it.
func (s *Session) History(withTools bool) []deepseek.Message {
	replay := withTools || s.UsedTools
	out := make([]deepseek.Message, 0, len(s.Messages))
	for _, m := range s.Messages {
		if !replay && m.Role == "assistant" {
			m.ReasoningContent = ""
		}
		out = append(out, m)
	}
	return out
}

// Meta is one line of `deepseek session ls`.
type Meta struct {
	Name     string    `json:"name"`
	Model    string    `json:"model,omitempty"`
	Turns    int       `json:"turns"`
	Updated  time.Time `json:"updated"`
	SizeByte int64     `json:"bytes"`
}

// List returns stored sessions, most recently used first.
func List() ([]Meta, error) {
	entries, err := os.ReadDir(dir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var out []Meta
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		s, err := Load(name)
		if err != nil {
			continue
		}
		m := Meta{Name: name, Model: s.Model, Turns: len(s.Messages), Updated: s.Updated}
		if info, err := entry.Info(); err == nil {
			m.SizeByte = info.Size()
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// Remove deletes a session. Removing one that is not there is not an
// error — the desired end state already holds.
func Remove(name string) error {
	p, err := path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
