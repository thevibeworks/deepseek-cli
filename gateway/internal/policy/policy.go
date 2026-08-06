// Package policy decides which requests the free tier will carry and
// rewrites them so they stay inside their allowance.
//
// The gateway is a transparent proxy — the CLI already speaks four wire
// formats against one base URL, and reimplementing them here would drift
// from upstream the day it shipped. So this package is deliberately the
// only place that looks inside a request body, and it changes as little
// as it can get away with: the model, the output cap, the concurrency
// fan-out, and the user identity.
package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Format is which wire format a route speaks. The formats disagree about
// where the output cap and the user identity live, which is the entire
// reason this type exists.
type Format int

const (
	FormatNone Format = iota // no body to rewrite
	FormatOpenAI
	FormatAnthropic
	FormatResponses
)

// Route is one endpoint the gateway is willing to proxy.
type Route struct {
	Name     string
	Method   string
	Upstream string
	Format   Format
	// AnthropicAuth sends x-api-key instead of a bearer token.
	AnthropicAuth bool
}

// routes is the allowlist. Anything not named here is refused rather
// than forwarded: an endpoint we have not thought about is an endpoint
// whose cost we cannot bound.
var routes = map[string]Route{
	"POST /chat/completions":      {Name: "chat", Method: "POST", Upstream: "/chat/completions", Format: FormatOpenAI},
	"POST /beta/chat/completions": {Name: "chat", Method: "POST", Upstream: "/beta/chat/completions", Format: FormatOpenAI},
	"POST /completions":           {Name: "fim", Method: "POST", Upstream: "/beta/completions", Format: FormatOpenAI},
	"POST /beta/completions":      {Name: "fim", Method: "POST", Upstream: "/beta/completions", Format: FormatOpenAI},
	"POST /responses":             {Name: "responses", Method: "POST", Upstream: "/responses", Format: FormatResponses},
	"POST /anthropic/v1/messages": {Name: "anthropic", Method: "POST", Upstream: "/anthropic/v1/messages", Format: FormatAnthropic, AnthropicAuth: true},
	"GET /models":                 {Name: "models", Method: "GET", Upstream: "/models", Format: FormatNone},
}

// Lookup resolves a client request path to a route.
//
// The "/v1" prefix is accepted and stripped because DeepSeek serves the
// OpenAI format at both /v1/chat/completions and /chat/completions, and
// half the OpenAI-compatible clients in the world append it themselves.
// A user pointing an existing tool at the gateway should not have to
// know which half theirs is in.
func Lookup(method, path string) (Route, bool) {
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		path = "/"
	}
	if trimmed := strings.TrimPrefix(path, "/v1"); trimmed != path && strings.HasPrefix(trimmed, "/") {
		path = trimmed
	}
	r, ok := routes[method+" "+path]
	return r, ok
}

// Limits are the per-request caps.
type Limits struct {
	// MaxTokens is the ceiling on generated tokens. A request asking for
	// more is clamped down; a request asking for nothing gets this.
	MaxTokens int
	// Model is the only model the free tier serves.
	Model string
}

// Decision is what came out of applying policy to a request.
type Decision struct {
	Body []byte
	// Model is what will actually run, for pricing.
	Model string
	// MaxTokens is the clamped output cap, used to bound the pessimistic
	// estimate if the response turns out to be unmeterable.
	MaxTokens int
	Stream    bool
}

// Reject is a request refused before it cost anything.
type Reject struct {
	Message string
	// Hint is the next thing to try, and is shown to the user verbatim.
	Hint string
}

func (r *Reject) Error() string { return r.Message }

// Apply validates and rewrites a request body.
//
// subject becomes DeepSeek's user_id, which is not a nicety: it is the
// mechanism their docs specify for one account fronting many end users,
// and it buys content-safety attribution, KV cache isolation between
// strangers, and per-user scheduling. Any user identity the client sent
// is overwritten — a caller who could choose its own could aim at
// someone else's cache namespace or poison their safety record.
func Apply(route Route, body []byte, subject string, lim Limits) (*Decision, error) {
	d := &Decision{Model: lim.Model, MaxTokens: lim.MaxTokens}
	if route.Format == FormatNone {
		d.Body = body
		return d, nil
	}

	obj, err := decodeObject(body)
	if err != nil {
		return nil, &Reject{Message: "request body is not a JSON object: " + err.Error()}
	}

	if err := checkModel(obj, lim.Model); err != nil {
		return nil, err
	}
	// The free tier serves one model. Pinning it here means a request that
	// omitted the field cannot inherit an upstream default we did not price.
	obj["model"] = lim.Model

	d.Stream = isTrue(obj["stream"])
	d.MaxTokens = clampOutput(obj, route.Format, lim.MaxTokens)
	if err := forbidFanOut(obj); err != nil {
		return nil, err
	}
	if err := forbidServerTools(obj, route.Format); err != nil {
		return nil, err
	}
	setIdentity(obj, route.Format, subject)

	out, err := json.Marshal(obj)
	if err != nil {
		return nil, &Reject{Message: "could not re-encode the request"}
	}
	d.Body = out
	return d, nil
}

func decodeObject(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	// Numbers stay as their original literals. Round-tripping through
	// float64 would rewrite 1 as 1 but 1e9 as 1000000000, and temperature
	// 0.1 as 0.1 — mostly harmless, but this proxy's promise is that the
	// bytes it forwards are the bytes you sent apart from the fields
	// named in this file.
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("body is null")
	}
	return obj, nil
}

// checkModel refuses anything that is not the free model.
//
// It refuses rather than silently downgrading. A user who asked for pro
// and got flash without being told would compare the answer against pro's
// reputation and conclude the model is worse than it is.
func checkModel(obj map[string]any, free string) error {
	asked, _ := obj["model"].(string)
	if asked == "" || asked == free {
		return nil
	}
	if resolve(asked) == free {
		// A Claude name the Anthropic endpoint maps onto flash anyway.
		return nil
	}
	return &Reject{
		Message: fmt.Sprintf("the free tier serves %s only, not %q", free, asked),
		Hint:    "bring your own key for " + strings.TrimSpace(strings.Replace(asked, free, "", 1)) + ": https://platform.deepseek.com/api_keys",
	}
}

// resolve mirrors the CLI's model resolution: the Anthropic endpoint
// accepts Claude names and remaps them server-side.
func resolve(model string) string {
	switch {
	case model == "deepseek-v4-flash" || model == "deepseek-v4-pro":
		return model
	case strings.HasPrefix(model, "claude-opus"):
		return "deepseek-v4-pro"
	default:
		return "deepseek-v4-flash"
	}
}

// outputField is where each format keeps its generated-token cap.
func outputField(f Format) string {
	switch f {
	case FormatResponses:
		return "max_output_tokens"
	default:
		return "max_tokens"
	}
}

// clampOutput holds the output cap at or below the free-tier ceiling and
// returns what it settled on. This is what bounds a single request's cost,
// and therefore how far the daily budget can overshoot.
func clampOutput(obj map[string]any, f Format, ceiling int) int {
	field := outputField(f)
	asked, ok := asInt(obj[field])
	if !ok || asked > ceiling || asked <= 0 {
		obj[field] = json.Number(itoa(ceiling))
		return ceiling
	}
	return asked
}

// forbidFanOut refuses the parameters that multiply one prompt into many
// billed completions. They are rare, and each one silently multiplies the
// cost of a request that has already passed the size check.
func forbidFanOut(obj map[string]any) error {
	for _, field := range []string{"n", "best_of"} {
		if v, ok := asInt(obj[field]); ok && v > 1 {
			return &Reject{
				Message: fmt.Sprintf("the free tier does not serve %s=%d", field, v),
				Hint:    "one completion per request; send the request twice if you need two",
			}
		}
	}
	return nil
}

// forbidServerTools refuses tools that run on DeepSeek's side. Client
// tools ("function") only declare a schema and cost nothing extra; a
// server-side tool like web_search performs billed work that never
// appears in the usage object, which would put its cost outside every
// ceiling this gateway enforces. Only the Responses format offers them.
func forbidServerTools(obj map[string]any, f Format) error {
	if f != FormatResponses {
		return nil
	}
	tools, _ := obj["tools"].([]any)
	for _, t := range tools {
		tool, _ := t.(map[string]any)
		kind, _ := tool["type"].(string)
		if kind != "" && kind != "function" {
			return &Reject{
				Message: fmt.Sprintf("the free tier does not serve server-side tools (%q)", kind),
				Hint:    "bring your own key for web search: https://platform.deepseek.com/api_keys",
			}
		}
	}
	return nil
}

// setIdentity stamps the subject onto the request in whichever field the
// format actually reads.
//
// Where each one lives, measured against the live API on 2026-08-05:
//
//	chat, FIM   user_id            documented in quick_start/rate_limit
//	Anthropic   metadata.user_id   documented in quick_start/rate_limit
//	Responses   user               undocumented; it is echoed back in the
//	                               response, while user_id and metadata
//	                               are silently dropped
//
// The Responses row is inference from an echo, not a promise: it is the
// only one of the three where DeepSeek has not said what the field does.
// Both names are set there so that whichever one they honour, we are
// covered.
func setIdentity(obj map[string]any, f Format, subject string) {
	switch f {
	case FormatAnthropic:
		meta, _ := obj["metadata"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
		}
		meta["user_id"] = subject
		obj["metadata"] = meta
		delete(obj, "user_id")
	case FormatResponses:
		obj["user"] = subject
		obj["user_id"] = subject
	default:
		obj["user_id"] = subject
		delete(obj, "user")
	}
}

func isTrue(v any) bool { b, _ := v.(bool); return b }

func asInt(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	if err != nil {
		return 0, false
	}
	return int(i), true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
