package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Model is one entry from GET /models.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// ModelList is the GET /models response.
type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// Models lists the models the key can reach.
func (c *Client) Models(ctx context.Context) (*ModelList, []byte, error) {
	raw, err := c.do(ctx, "GET", "/models", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	var out ModelList
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("decoding model list: %w", err)
	}
	return &out, raw, nil
}

// BalanceInfo is the balance in one currency. The API reports the amounts
// as strings; they are kept as strings here so a figure the API sent is
// never reprinted with different precision than it arrived with.
type BalanceInfo struct {
	Currency        string `json:"currency"`
	TotalBalance    string `json:"total_balance"`
	GrantedBalance  string `json:"granted_balance"`
	ToppedUpBalance string `json:"topped_up_balance"`
}

// Amount parses the total balance for comparison. ok is false if the API
// sent something unparseable, in which case callers should show the
// string rather than a number.
func (b BalanceInfo) Amount() (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(b.TotalBalance), 64)
	return v, err == nil
}

// Balance is the GET /user/balance response. Accounts can hold more than
// one currency at once — a live account returns both a USD and a CNY row —
// so this is always a list, never a single figure.
type Balance struct {
	IsAvailable  bool          `json:"is_available"`
	BalanceInfos []BalanceInfo `json:"balance_infos"`
}

// Funded returns the currencies with a non-zero balance.
func (b *Balance) Funded() []BalanceInfo {
	var out []BalanceInfo
	for _, info := range b.BalanceInfos {
		if v, ok := info.Amount(); ok && v > 0 {
			out = append(out, info)
		}
	}
	return out
}

// Balance fetches the account balance.
func (c *Client) Balance(ctx context.Context) (*Balance, []byte, error) {
	raw, err := c.do(ctx, "GET", "/user/balance", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	var out Balance
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, raw, fmt.Errorf("decoding balance: %w", err)
	}
	return &out, raw, nil
}

// Raw sends an arbitrary request to an arbitrary path with this client's
// auth. It exists so that an endpoint DeepSeek ships tomorrow is reachable
// today, without waiting for a release of this tool. Everything else in
// this package is a typed convenience over exactly this.
func (c *Client) Raw(ctx context.Context, method, path string, body json.RawMessage, anthropicAuth bool) ([]byte, error) {
	var hdr map[string]string
	if anthropicAuth {
		hdr = c.anthropicHeaders()
	}
	var payload any
	if len(body) > 0 {
		payload = body
	}
	return c.do(ctx, method, path, payload, hdr)
}
