// Package docs carries DeepSeek's own API documentation inside the
// binary, so the CLI can answer questions about the API it talks to.
//
// The corpus is the English half of the markdown mirror at
// github.com/thevibeworks/deepseek-docs — the api-docs.deepseek.com site
// converted page by page, plus the FAQ, which lives outside that site as
// a JSON blob in a JS bundle and is not otherwise readable as text.
//
// Two copies exist and the newer wins:
//
//   - the embedded snapshot, ~85KB gzipped, so a fresh binary answers
//     offline and on first run;
//   - a cache in the state directory, refreshed by `deepseek docs sync`,
//     because DeepSeek ships model and pricing changes faster than this
//     CLI ships releases.
//
// Every page keeps the `source:` URL it was converted from, and every
// answer built on this corpus cites it. A local copy of someone else's
// documentation is a liability the moment it stops saying where it came
// from and how old it is.
package docs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed corpus.tar.gz
var embedded embed.FS

// Page is one documentation page.
type Page struct {
	// Path is the corpus-relative path without the extension, and the
	// name used to address a page: "guides/thinking_mode".
	Path        string `json:"path"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// Source is the upstream URL this page was converted from.
	Source string `json:"source,omitempty"`
	// Fetched is when the mirror last read that URL.
	Fetched string `json:"fetched,omitempty"`
	Body    string `json:"body,omitempty"`
}

// Corpus is a loaded set of pages.
type Corpus struct {
	Pages []Page
	// Origin is "embedded" or the path of the cache that superseded it.
	Origin string
	// Fetched is the newest fetch date across the pages, which is how old
	// the corpus is as a whole.
	Fetched string

	byPath map[string]*Page
}

var (
	loadOnce sync.Once
	loaded   *Corpus
	loadErr  error
)

// Load returns the corpus, preferring a synced cache over the snapshot
// compiled into the binary. It is read once per process.
func Load() (*Corpus, error) {
	loadOnce.Do(func() {
		if path := CachePath(); path != "" {
			if b, err := os.ReadFile(path); err == nil {
				if c, err := parse(b, path); err == nil && len(c.Pages) > 0 {
					loaded = c
					return
				}
			}
		}
		b, err := embedded.ReadFile("corpus.tar.gz")
		if err != nil {
			loadErr = fmt.Errorf("no documentation in this binary: %w", err)
			return
		}
		loaded, loadErr = parse(b, "embedded")
	})
	return loaded, loadErr
}

// CachePath is where `docs sync` writes the refreshed corpus.
func CachePath() string { return filepath.Join(stateDir(), "docs.tar.gz") }

// Inspect parses a downloaded corpus without installing it, so a caller
// can compare it against the one in use before overwriting anything.
func Inspect(b []byte) (*Corpus, error) {
	c, err := parse(b, CachePath())
	if err != nil {
		return nil, fmt.Errorf("the downloaded corpus did not parse: %w", err)
	}
	if len(c.Pages) == 0 {
		return nil, fmt.Errorf("the downloaded corpus has no pages")
	}
	return c, nil
}

// SaveCache installs a downloaded corpus, after checking it parses. A
// sync that leaves the CLI unable to read its own docs is worse than a
// sync that failed.
func SaveCache(b []byte) (*Corpus, error) {
	c, err := Inspect(b)
	if err != nil {
		return nil, err
	}
	path := CachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".docs-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, err
	}
	return c, nil
}

// parse reads the gzipped tar into pages.
func parse(b []byte, origin string) (*Corpus, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	c := &Corpus{Origin: origin, byPath: map[string]*Page{}}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name, ok := corpusPath(hdr.Name)
		if hdr.Typeflag != tar.TypeReg || !ok {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		c.Pages = append(c.Pages, newPage(name, string(body)))
	}
	sort.Slice(c.Pages, func(i, j int) bool { return c.Pages[i].Path < c.Pages[j].Path })
	for i := range c.Pages {
		c.byPath[c.Pages[i].Path] = &c.Pages[i]
		if f := c.Pages[i].Fetched; f > c.Fetched {
			c.Fetched = f
		}
	}
	return c, nil
}

// corpusPath normalises an archive entry to a page path, and reports
// whether it is one at all.
//
// Two archive layouts have to work. `make corpus` packs the mirror's
// content directory, so entries look like "en/guides/x.md". A `docs
// sync` may instead be pointed at GitHub's repository tarball, where the
// same file is "deepseek-docs-main/content/en/guides/x.md". Anchoring on
// the "en/" segment accepts both without the caller choosing.
func corpusPath(name string) (string, bool) {
	name = filepath.ToSlash(name)
	if !strings.HasSuffix(name, ".md") {
		return "", false
	}
	switch {
	case strings.HasPrefix(name, "en/"):
		return strings.TrimSuffix(name[len("en/"):], ".md"), true
	default:
		if i := strings.Index(name, "/en/"); i >= 0 {
			return strings.TrimSuffix(name[i+len("/en/"):], ".md"), true
		}
	}
	// Any other locale, or a file outside the docs tree.
	return "", false
}

// newPage splits a mirrored file into its frontmatter and its body. The
// frontmatter is the mirror's own, four known keys, so this reads them
// directly rather than pulling in a YAML parser for the purpose.
func newPage(path, text string) Page {
	p := Page{Path: path, Body: text}

	if !strings.HasPrefix(text, "---\n") {
		p.Title = path
		return p
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		p.Title = path
		return p
	}
	front, body := text[4:4+end], text[4+end+5:]
	p.Body = body
	for _, line := range strings.Split(front, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.TrimSuffix(strings.TrimPrefix(value, `"`), `"`)
		switch strings.TrimSpace(key) {
		case "title":
			p.Title = value
		case "description":
			p.Description = value
		case "source":
			p.Source = value
		case "fetched":
			p.Fetched = value
		}
	}
	if p.Title == "" {
		p.Title = path
	}
	return p
}

// Get returns one page by path. A path may be given with or without the
// .md suffix, and a unique suffix match is accepted — "thinking_mode"
// finds "guides/thinking_mode", because nobody remembers the directory.
func (c *Corpus) Get(path string) (*Page, error) {
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".md")
	if p, ok := c.byPath[path]; ok {
		return p, nil
	}
	var matches []*Page
	for i := range c.Pages {
		p := &c.Pages[i]
		if strings.HasSuffix(p.Path, "/"+path) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("no page %q — try: deepseek docs search %s", path, path)
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Path
		}
		return nil, fmt.Errorf("%q matches %s", path, strings.Join(names, ", "))
	}
}

// Age reports how long ago the corpus was fetched, and whether that could
// be determined at all.
func (c *Corpus) Age() (time.Duration, bool) {
	if c.Fetched == "" {
		return 0, false
	}
	t, err := time.Parse("2006-01-02", c.Fetched)
	if err != nil {
		return 0, false
	}
	return time.Since(t), true
}

func stateDir() string {
	if v := os.Getenv("DEEPSEEK_STATE_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "deepseek")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".deepseek"
	}
	return filepath.Join(home, ".local", "state", "deepseek")
}

// ensure the embed directive keeps a real file behind it.
var _ fs.FS = embedded
