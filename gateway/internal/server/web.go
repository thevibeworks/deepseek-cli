package server

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

// The dashboard, and the pages that explain what this service is and what
// it does with a prompt.
//
// They are embedded rather than served from disk so that the gateway
// stays one binary with nothing beside it — the same property that earns
// it a place on a box running someone else's production. It also means
// the page and the JSON it reads can never be different versions of each
// other, which is the usual way a status page starts lying.
//
//go:embed web/index.html web/style.css web/app.js web/pages/*.html
var webFS embed.FS

// webCacheSec is how long a browser may hold these files. Short, because
// the whole point of a status page is that it is current, and the assets
// are a few kilobytes on a service that serves megabytes of tokens.
const webCacheSec = 300

// routeWeb mounts the site. Every path is explicit: a file server rooted
// at an embedded directory would happily serve anything that later lands
// in it, and this binary holds an API key.
func (s *Server) routeWeb(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.serveAsset("web/index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /style.css", s.serveAsset("web/style.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /app.js", s.serveAsset("web/app.js", "text/javascript; charset=utf-8"))

	for _, page := range []string{"privacy", "terms", "story", "vision", "economics"} {
		h := s.serveAsset("web/pages/"+page+".html", "text/html; charset=utf-8")
		mux.HandleFunc("GET /"+page, h)
		mux.HandleFunc("GET /"+page+"/", h)
	}

	mux.HandleFunc("GET /robots.txt", s.serveRobots)
	mux.HandleFunc("GET /sitemap.xml", s.serveSitemap)
}

func (s *Server) serveAsset(name, contentType string) http.HandlerFunc {
	body, err := webFS.ReadFile(name)
	return func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			// A missing asset is a build mistake, not a runtime condition.
			// Saying so beats serving an empty page that looks like an
			// outage.
			writeError(w, http.StatusInternalServerError, typeInternal,
				"this build is missing "+name)
			return
		}
		h := w.Header()
		h.Set("Content-Type", contentType)
		h.Set("Cache-Control", fmt.Sprintf("public, max-age=%d", webCacheSec))
		// The page loads nothing from anywhere else and posts nowhere, so
		// it can say so. connect-src stays 'self' because the dashboard
		// polls its own /v1/status.
		h.Set("Content-Security-Policy",
			"default-src 'none'; style-src 'self'; script-src 'self' 'unsafe-inline'; "+
				"connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		w.Write(body)
	}
}

func (s *Server) serveRobots(w http.ResponseWriter, r *http.Request) {
	base := s.publicBase()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// The API paths are not content and crawling them wastes the pool's
	// rate limits on robots. The pages are the point, so they stay open.
	fmt.Fprintf(w, "User-agent: *\nAllow: /$\nAllow: /privacy\nAllow: /terms\nAllow: /story\nAllow: /vision\nAllow: /economics\n"+
		"Disallow: /v1/\nDisallow: /admin/\nDisallow: /chat/\nDisallow: /responses\nDisallow: /anthropic/\n\nSitemap: %s/sitemap.xml\n", base)
}

func (s *Server) serveSitemap(w http.ResponseWriter, r *http.Request) {
	base := s.publicBase()
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range []struct {
		path string
		pri  string
		freq string
	}{
		{"/", "1.0", "hourly"},
		{"/story", "0.8", "monthly"},
		{"/vision", "0.8", "monthly"},
		{"/economics", "0.8", "monthly"},
		{"/privacy", "0.5", "yearly"},
		{"/terms", "0.5", "yearly"},
	} {
		fmt.Fprintf(&b, "  <url><loc>%s%s</loc><changefreq>%s</changefreq><priority>%s</priority></url>\n",
			base, p.path, p.freq, p.pri)
	}
	b.WriteString("</urlset>\n")
	io.WriteString(w, b.String())
}

// publicBase is the URL this service is reached at, for the absolute URLs
// a sitemap and a canonical tag require.
func (s *Server) publicBase() string {
	if s.cfg.Announce != "" {
		return strings.TrimRight(s.cfg.Announce, "/")
	}
	return "https://freeseek.1lm.io"
}

// --- small shared helpers ------------------------------------------------

// decodeJSON reads a size-limited JSON body.
func decodeJSON(r *http.Request, limit int64, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("could not read the request: %w", err)
	}
	return nil
}

// subtleEqual compares two secrets in constant time, so an admin token
// cannot be recovered a byte at a time by timing the comparison.
func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// assetNames lists what got embedded, for the build-time test that keeps
// this file and the web directory honest with each other.
func assetNames() []string {
	var out []string
	fs.WalkDir(webFS, "web", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}
