package deepseek

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// ErrNoKey is returned when no API key could be resolved. It carries its
// own fix instructions because "unauthorized" with no next step is the
// least useful error a CLI can print.
var ErrNoKey = errors.New("no API key — set DEEPSEEK_API_KEY, pass --api-key, or write one to " + KeyFile())

// APIError is a 4xx/5xx response from DeepSeek. Every endpoint, in every
// wire format, returns the same envelope:
//
//	{"error":{"message":..., "type":..., "param":..., "code":...}}
type APIError struct {
	StatusCode int
	Method     string
	URL        string
	Message    string
	Type       string
	Code       string
	Param      string
	RetryAfter time.Duration
	// Raw is the untouched response body, so --json can show exactly what
	// the API said rather than our reading of it.
	Raw []byte
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	if hint := e.Hint(); hint != "" {
		return fmt.Sprintf("%s (HTTP %d)\n  %s", msg, e.StatusCode, hint)
	}
	return fmt.Sprintf("%s (HTTP %d)", msg, e.StatusCode)
}

// Hint maps DeepSeek's documented error codes to the action that fixes
// them. The API's own message says what went wrong; this says what to do.
func (e *APIError) Hint() string {
	switch e.StatusCode {
	case http.StatusUnauthorized:
		return "check your key: deepseek check — or set DEEPSEEK_API_KEY from https://platform.deepseek.com/api_keys"
	case http.StatusPaymentRequired:
		return "out of balance: deepseek balance — top up at https://platform.deepseek.com/top_up"
	case http.StatusUnprocessableEntity, http.StatusBadRequest:
		return "the request was rejected as invalid; run with --verbose --verbose to see the body that was sent"
	case http.StatusTooManyRequests:
		return "concurrency limit reached (2500 flash / 500 pro per account); pace the requests and retry"
	case http.StatusInternalServerError:
		return "server error on DeepSeek's side; retry shortly"
	case http.StatusServiceUnavailable:
		return "DeepSeek is overloaded; retry shortly"
	}
	return ""
}

func newAPIError(resp *http.Response, raw []byte) *APIError {
	e := &APIError{
		StatusCode: resp.StatusCode,
		Method:     resp.Request.Method,
		URL:        resp.Request.URL.String(),
		Raw:        raw,
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Param   any    `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		e.Message = envelope.Error.Message
		e.Type = envelope.Error.Type
		e.Code = fmt.Sprint(orEmpty(envelope.Error.Code))
		e.Param = fmt.Sprint(orEmpty(envelope.Error.Param))
	}
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return e
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

// TransportError is a failure that never reached the API: DNS, TLS,
// connection reset, timeout. Distinguished from APIError because a
// transport failure is always safe to retry and never costs money.
type TransportError struct {
	Op  string
	Err error
}

func (e *TransportError) Error() string { return e.Op + ": " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// Retryable reports whether another attempt could plausibly succeed.
// Transport failures always qualify; of the API's own errors only
// rate-limit and server-side faults do. A 400 retried is a 400.
func Retryable(err error) bool {
	var te *TransportError
	if errors.As(err, &te) {
		return true
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.StatusCode {
		case http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			return true
		}
	}
	return false
}

// Exit codes. Scripts branch on these, so they are part of the contract:
// 2 means "your credentials are wrong", 3 means "you have no money", and
// both are worth handling differently from a generic failure.
const (
	ExitOK        = 0
	ExitError     = 1
	ExitAuth      = 2
	ExitBalance   = 3
	ExitRateLimit = 4
)

// ExitCode classifies an error for os.Exit.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, ErrNoKey) {
		return ExitAuth
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ExitAuth
		case http.StatusPaymentRequired:
			return ExitBalance
		case http.StatusTooManyRequests:
			return ExitRateLimit
		}
	}
	return ExitError
}

// StateDir is where the CLI keeps things it wrote itself: the usage
// ledger and saved conversations. XDG first, with a plain ~/.deepseek
// fallback for machines that set neither.
func StateDir() string {
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

// ConfigDir is where the user puts things: the API key file.
func ConfigDir() string {
	if v := os.Getenv("DEEPSEEK_CONFIG_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "deepseek")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".deepseek"
	}
	return filepath.Join(home, ".config", "deepseek")
}

// KeyFile is the optional on-disk API key, one line, mode 0600.
func KeyFile() string { return filepath.Join(ConfigDir(), "api_key") }
