package docs

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Search over the corpus.
//
// This is deliberately a scored substring search and not an index. The
// corpus is sixty-odd pages and half a megabyte; a full scan takes about
// a millisecond, and any structure beyond that would be machinery to
// maintain in exchange for nothing a user could perceive.
//
// What matters is the ranking, because "where is this documented" is
// answered by the right page being first. A term in a title is worth far
// more than the same term buried in a code sample, so matches are
// weighted by where they land.

// Hit is one search result.
type Hit struct {
	Page    *Page  `json:"-"`
	Path    string `json:"path"`
	Title   string `json:"title"`
	Source  string `json:"source,omitempty"`
	Score   int    `json:"score"`
	Snippet string `json:"snippet,omitempty"`
}

const (
	weightTitle       = 40
	weightDescription = 12
	weightHeading     = 8
	weightBody        = 12

	// BM25's two constants, at their usual values. k1 sets how fast
	// repeated mentions stop counting; b how hard length is penalised.
	//
	// Both earn their place here. One page of this corpus —
	// quick_start/agent_integrations/codex — is 82KB, three times the
	// next largest, and under raw term counts it wins almost any query
	// containing an ordinary English word. Asked "when must I send
	// reasoning_content back", unnormalised scoring returned that page
	// alone and the answer came back "the documentation does not mention
	// reasoning_content", while guides/thinking_mode explains the rule at
	// length. Saturation and length normalisation are what put the right
	// page first.
	k1 = 1.5
	b  = 0.75
)

// Search returns the pages best covering the query, best first.
//
// Coverage is weighted rather than required, and that distinction was
// bought with a wrong answer. Requiring every term looks tidy until a
// question like "what is the max output token limit for FIM" excludes
// guides/fim_completion — the one page that states the 4K cap — because
// it never uses the word "output". Strict AND then hands the model a
// pile of release notes, and the model, correctly refusing to invent,
// reports that the documentation does not say. It does say.
//
// So a page matching some terms competes, scaled down hard by what it
// missed: score * (matched/total)^2. A page with the subject in its
// title beats a page that mentions every word once, which is the
// ordering a reader would pick by hand.
func (c *Corpus) Search(query string, limit int) []Hit {
	terms := terms(query)
	if len(terms) == 0 {
		return nil
	}

	idf := c.idf(terms)
	avgLen := c.avgLen()

	var hits []Hit
	for i := range c.Pages {
		p := &c.Pages[i]
		lowerTitle := strings.ToLower(p.Title + " " + p.Path)
		lowerDesc := strings.ToLower(p.Description)
		lowerBody := strings.ToLower(p.Body)
		// BM25's length term: a long page needs proportionally more hits
		// to mean the same thing.
		norm := k1 * (1 - b + b*float64(len(p.Body))/avgLen)

		raw, matched := 0.0, 0
		for _, term := range terms {
			bodyTF := float64(strings.Count(lowerBody, term))
			// Saturating: the 20th mention of a word tells you almost
			// nothing the 5th did not.
			bodyScore := weightBody * bodyTF * (k1 + 1) / (bodyTF + norm)

			// The short fields are bounded by construction, so they are
			// counted straight — but capped, so a path that repeats a word
			// cannot outweigh the page that explains it.
			fieldScore := float64(min(strings.Count(lowerTitle, term), 2)*weightTitle +
				min(strings.Count(lowerDesc, term), 2)*weightDescription +
				min(countHeadings(p.Body, term), 4)*weightHeading)

			total := bodyScore + fieldScore
			if total == 0 {
				continue
			}
			matched++
			raw += total * idf[term]
		}
		if matched == 0 {
			continue
		}
		coverage := float64(matched) / float64(len(terms))
		score := int(raw * coverage * coverage * canonicity(p.Path))
		if score == 0 {
			score = 1
		}
		hits = append(hits, Hit{
			Page: p, Path: p.Path, Title: p.Title, Source: p.Source,
			Score: score, Snippet: snippet(p.Body, terms),
		})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Path < hits[j].Path
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// idf weights each term by how rare it is in the corpus.
//
// Without it, "max output token limit for FIM" is five equal words, and
// the four generic ones — which appear on nearly every page of an API
// reference — drown the one that identifies the subject. "fim" is on a
// handful of pages and "token" is on most of them; the query is about
// FIM. Inverse document frequency is the standard way to say so, and on
// a corpus this size it costs one pass.
func (c *Corpus) idf(terms []string) map[string]float64 {
	out := make(map[string]float64, len(terms))
	n := float64(len(c.Pages))
	for _, term := range terms {
		df := 0
		for i := range c.Pages {
			p := &c.Pages[i]
			if strings.Contains(strings.ToLower(p.Title+" "+p.Path+" "+p.Body), term) {
				df++
			}
		}
		// Smoothed, and floored at 1 so a term on every page still counts
		// for something rather than zeroing an otherwise good match.
		out[term] = 1 + math.Log(n/float64(1+df))
		if out[term] < 1 {
			out[term] = 1
		}
	}
	return out
}

// terms splits a query into lowercase words, dropping the stopwords that
// would otherwise match every page. A question typed at a prompt — "how
// do I turn off thinking" — should search for "turn off thinking".
func terms(query string) []string {
	var out []string
	for _, field := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
	}) {
		if stopwords[field] || len(field) < 2 {
			continue
		}
		out = append(out, field)
	}
	return out
}

var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "but": true, "by": true, "can": true, "do": true, "does": true,
	"for": true, "from": true, "get": true, "how": true, "i": true, "if": true,
	"in": true, "is": true, "it": true, "me": true, "my": true, "of": true,
	"on": true, "or": true, "should": true, "that": true, "the": true,
	"there": true, "this": true, "to": true, "use": true, "what": true,
	"when": true, "where": true, "which": true, "why": true, "with": true,
	"you": true, "your": true,
}

// countHeadings counts term occurrences in markdown headings, which are
// the closest thing the corpus has to a table of contents.
func countHeadings(body, term string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		n += strings.Count(strings.ToLower(line), term)
	}
	return n
}

// snippet returns the line best covering the query, trimmed to fit a
// terminal, with enough context to judge the hit without opening it.
func snippet(body string, terms []string) string {
	best, bestScore := "", 0
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < 20 || strings.HasPrefix(trimmed, "```") {
			continue
		}
		lower := strings.ToLower(trimmed)
		score := 0
		for _, term := range terms {
			if strings.Contains(lower, term) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = trimmed, score
		}
		if bestScore == len(terms) {
			break
		}
	}
	best = strings.TrimLeft(best, "#|-* ")
	if len(best) > 160 {
		best = best[:157] + "..."
	}
	return best
}

// Context assembles the pages behind a set of hits into one block for a
// model to read, capped so a question about the API does not send the
// whole manual.
//
// Pages go in whole rather than as excerpts: the answer to "does FIM
// support thinking" is often a footnote three paragraphs from the term
// that matched, and a retrieval window that clips it produces a confident
// wrong answer. Order is by score, so the cap falls on the least relevant
// page, and it is stable for the same query — which is what lets
// DeepSeek's context cache pay for a follow-up question.
func Context(hits []Hit, maxBytes int) (text string, used []Hit) {
	var b strings.Builder
	for _, h := range hits {
		page := h.Page.Body
		entry := "\n\n---\n\n# " + h.Page.Title + "\nsource: " + h.Page.Source + "\npath: " + h.Page.Path + "\n\n" + page
		if b.Len() > 0 && b.Len()+len(entry) > maxBytes {
			continue
		}
		b.WriteString(entry)
		used = append(used, h)
	}
	return strings.TrimSpace(b.String()), used
}

// avgLen is the mean page length, BM25's reference point for "normal".
func (c *Corpus) avgLen() float64 {
	if len(c.Pages) == 0 {
		return 1
	}
	total := 0
	for i := range c.Pages {
		total += len(c.Pages[i].Body)
	}
	return float64(total) / float64(len(c.Pages))
}

// canonicity down-weights the parts of the corpus that describe what
// DeepSeek did rather than what it does.
//
// Release notes age into history but keep their keywords forever. Asked
// for "context caching", the corpus offers guides/kv_cache — the page
// that documents the current behaviour — and news/news0802, a 2024
// announcement whose headline happens to contain the exact phrase twice.
// On term scoring alone the announcement wins, and the reader gets
// history instead of the manual.
//
// Announcements are still reachable: they win when the query is about a
// release, they are listed whole by `docs changelog`, and a 0.7 nudge
// only reorders a near-tie. Nothing is excluded.
func canonicity(path string) float64 {
	switch {
	case strings.HasPrefix(path, "news/"):
		return 0.7
	case path == "updates":
		return 0.8
	default:
		return 1
	}
}
