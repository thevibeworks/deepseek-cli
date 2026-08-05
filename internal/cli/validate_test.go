package cli

import (
	"strings"
	"testing"

	"github.com/thevibeworks/deepseek-cli/internal/deepseek"
)

func TestValidateUserID(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		wantErr string
	}{
		{"empty is fine", "", ""},
		{"plain", "tenant-42", ""},
		{"underscores and digits", "acct_007", ""},
		{"at the limit", strings.Repeat("x", 512), ""},
		{"one over", strings.Repeat("x", 513), "at most 512"},
		{"space", "two words", "letters, digits"},
		{"email, which is also a privacy problem", "a@b.com", "letters, digits"},
		{"slash", "org/team", "letters, digits"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUserID(tc.id)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("validateUserID(%.20q) = %v, want nil", tc.id, err)
			case tc.wantErr != "" && err == nil:
				t.Errorf("validateUserID(%.20q) = nil, want an error about %q", tc.id, tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("validateUserID(%.20q) = %v, want it to mention %q", tc.id, err, tc.wantErr)
			}
		})
	}
}

func TestValidateTools(t *testing.T) {
	tool := func(name string) deepseek.Tool {
		var t deepseek.Tool
		t.Type = "function"
		t.Function.Name = name
		return t
	}

	if err := validateTools([]deepseek.Tool{tool("get_weather"), tool("list-files")}); err != nil {
		t.Errorf("valid tools rejected: %v", err)
	}
	if err := validateTools(nil); err != nil {
		t.Errorf("no tools rejected: %v", err)
	}

	many := make([]deepseek.Tool, 129)
	for i := range many {
		many[i] = tool("f")
	}
	if err := validateTools(many); err == nil || !strings.Contains(err.Error(), "128") {
		t.Errorf("129 tools = %v, want the documented 128 cap", err)
	}

	if err := validateTools([]deepseek.Tool{tool(strings.Repeat("n", 65))}); err == nil {
		t.Error("a 65-character tool name was accepted; the API allows 64")
	}
	if err := validateTools([]deepseek.Tool{tool("get weather")}); err == nil {
		t.Error("a tool name with a space was accepted")
	}
}

// A strict tool must move the request onto the beta path. Getting this
// wrong is silent: the stable path ignores strict, the model answers
// anyway, and the schema guarantee the caller asked for simply is not
// there.
func TestAnyStrictSelectsBetaPath(t *testing.T) {
	plain := deepseek.Tool{}
	plain.Function.Name = "a"
	strict := deepseek.Tool{}
	strict.Function.Name = "b"
	strict.Function.Strict = true

	if anyStrict(nil) {
		t.Error("no tools should not select beta")
	}
	if anyStrict([]deepseek.Tool{plain}) {
		t.Error("a non-strict tool should not select beta")
	}
	if !anyStrict([]deepseek.Tool{plain, strict}) {
		t.Error("one strict tool among many must select beta")
	}
}

// The effort levels are the API's, established by probing it: everything
// here was accepted live on 2026-08-05, and only genuinely unknown values
// are rejected ("unknown variant"). Two of them — none and minimal — are
// in no documentation.
func TestValidEffortMatchesTheAPI(t *testing.T) {
	for _, e := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "HIGH", "Max"} {
		if err := validEffort(e); err != nil {
			t.Errorf("validEffort(%q) = %v, but the API accepts it", e, err)
		}
	}
	for _, e := range []string{"bogus", "highest", "1", ""} {
		if e == "" {
			continue // unset is handled before validation
		}
		if err := validEffort(e); err == nil {
			t.Errorf("validEffort(%q) = nil, but the API rejects it", e)
		}
	}
}

// The thinking surcharge is not the flat 79 tokens this project used to
// document. These numbers were measured against the live API at two
// prompt lengths, twice each; if DeepSeek changes the templates, this is
// where it shows up.
func TestThinkingTemplateMeasurements(t *testing.T) {
	cases := []struct {
		model, effort string
		want          int
	}{
		{deepseek.ModelFlash, "", 79}, // unset == high, the flash default
		{deepseek.ModelFlash, "none", 0},
		{deepseek.ModelFlash, "minimal", 0},
		{deepseek.ModelFlash, "low", 0},
		{deepseek.ModelFlash, "medium", 79},
		{deepseek.ModelFlash, "high", 79},
		{deepseek.ModelFlash, "xhigh", 79},
		{deepseek.ModelFlash, "max", 92},
		{deepseek.ModelPro, "", 0},
		{deepseek.ModelPro, "low", 0},
		{deepseek.ModelPro, "high", 0},
		{deepseek.ModelPro, "max", 79},
		// Claude names reach the pro rate card through the Anthropic
		// endpoint, so they must resolve to pro's templates too.
		{"claude-opus-4-1", "max", 79},
	}
	for _, tc := range cases {
		got := deepseek.ThinkingTemplate(tc.model, tc.effort)
		if got != tc.want {
			t.Errorf("ThinkingTemplate(%q, %q) = %d, measured %d", tc.model, tc.effort, got, tc.want)
		}
	}

	if !deepseek.EffortDisablesThinking("none") {
		t.Error("effort none disables thinking outright — measured identical to thinking:{disabled}")
	}
	if deepseek.EffortDisablesThinking("low") {
		t.Error("low still reasons; it only drops the template")
	}
}

func TestExpandModel(t *testing.T) {
	for in, want := range map[string]string{
		"flash":           deepseek.ModelFlash,
		"f":               deepseek.ModelFlash,
		"pro":             deepseek.ModelPro,
		"PRO":             deepseek.ModelPro,
		"deepseek-v4-pro": deepseek.ModelPro,
		"nonsense":        "nonsense", // left alone, rejected by the caller
	} {
		if got := expandModel(in); got != want {
			t.Errorf("expandModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMoneyNeverPrintsFreeForNonZero(t *testing.T) {
	// "$0.000000" reads as free. A cost too small to render at six
	// decimals is a different claim from no cost at all.
	if got := money(0.0000001); !strings.HasPrefix(got, "<") {
		t.Errorf("money(1e-7) = %q, want a less-than form", got)
	}
	if got := money(0); got != "$0" {
		t.Errorf("money(0) = %q, want $0", got)
	}
	for _, tc := range []struct {
		usd  float64
		want string
	}{
		{0.000559, "$0.000559"},
		{0.19, "$0.1900"},
		{12.5, "$12.50"},
	} {
		if got := money(tc.usd); got != tc.want {
			t.Errorf("money(%g) = %q, want %q", tc.usd, got, tc.want)
		}
	}
}
