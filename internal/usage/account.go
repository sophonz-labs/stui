package usage

// Whose limits these are. Limits belong to an Anthropic or OpenAI account,
// not to a machine, and several machines can share one; the apps merge
// what each machine reports by AccountInfo.ID. The ID is a hash and the
// label a masked address: neither goes anywhere but to the apps through the
// terminal's own address (and sealed, in usage-high).

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// AccountInfo is the account an agent's limits belong to.
type AccountInfo struct {
	// 16 hex: SHA-256 of the provider's account (and organization) ids.
	// The same on every machine signed in to the same account.
	ID string `json:"id"`
	// The address masked: "y***@example.com". "" when not known.
	Label string `json:"label,omitempty"`
	// "Max 20x", "Pro", "Plus", "Team". "" when not known.
	Plan string `json:"plan,omitempty"`
}

func accountID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:8])
}

// MaskEmail keeps the first character and the domain.
func MaskEmail(s string) string {
	s = strings.TrimSpace(s)
	at := strings.LastIndex(s, "@")
	if at <= 0 {
		return ""
	}
	r := []rune(s[:at])
	return string(r[0]) + "***" + s[at:]
}

// claudeJSON is where Claude Code keeps who is signed in: .claude.json
// beside the settings with CLAUDE_CONFIG_DIR, else in the home directory.
func claudeJSON(home, configDir string) string {
	if configDir != "" {
		return filepath.Join(configDir, ".claude.json")
	}
	return filepath.Join(home, ".claude.json")
}

// ClaudeAccount reads oauthAccount from .claude.json: its account and
// organization ids, address and plan. Nothing else of the file is kept.
func ClaudeAccount(home, configDir string) *AccountInfo {
	b, err := os.ReadFile(claudeJSON(home, configDir))
	if err != nil || len(b) > 64<<20 || !bytes.Contains(b, []byte(`"oauthAccount"`)) {
		return nil
	}
	var f struct {
		OAuth *struct {
			AccountUUID      string `json:"accountUuid"`
			OrganizationUUID string `json:"organizationUuid"`
			Email            string `json:"emailAddress"`
			OrgType          string `json:"organizationType"`
			RateLimitTier    string `json:"organizationRateLimitTier"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(b, &f) != nil || f.OAuth == nil || f.OAuth.AccountUUID == "" {
		return nil
	}
	o := f.OAuth
	return &AccountInfo{
		ID:    accountID("claude", o.AccountUUID, o.OrganizationUUID),
		Label: MaskEmail(o.Email),
		Plan:  claudePlan(o.OrgType, o.RateLimitTier),
	}
}

// claudePlan names the plan from organizationType (claude_max) and the
// rate limit tier (default_claude_max_20x).
func claudePlan(orgType, tier string) string {
	t := strings.ToLower(tier)
	switch {
	case strings.Contains(t, "max_20x"):
		return "Max 20x"
	case strings.Contains(t, "max_5x"):
		return "Max 5x"
	}
	switch strings.ToLower(orgType) {
	case "claude_max":
		return "Max"
	case "claude_pro":
		return "Pro"
	case "claude_team":
		return "Team"
	case "claude_enterprise":
		return "Enterprise"
	}
	return ""
}

// CodexAccount reads auth.json: the ChatGPT account id, and from the ID
// token's claims (the token itself is not kept or sent) the address and
// plan. An API-key login has no account and no plan limits.
func CodexAccount(codexDir string) *AccountInfo {
	b, err := os.ReadFile(filepath.Join(codexDir, "auth.json"))
	if err != nil {
		return nil
	}
	var f struct {
		Tokens *struct {
			IDToken   string `json:"id_token"`
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &f) != nil || f.Tokens == nil {
		return nil
	}
	var claims struct {
		Email string `json:"email"`
		Auth  struct {
			AccountID string `json:"chatgpt_account_id"`
			PlanType  string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if parts := strings.Split(f.Tokens.IDToken, "."); len(parts) == 3 {
		if p, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "=")); err == nil {
			json.Unmarshal(p, &claims)
		}
	}
	id := f.Tokens.AccountID
	if id == "" {
		id = claims.Auth.AccountID
	}
	if id == "" {
		return nil
	}
	return &AccountInfo{ID: accountID("codex", id), Label: MaskEmail(claims.Email), Plan: codexPlan(claims.Auth.PlanType)}
}

func codexPlan(p string) string {
	switch p = strings.ToLower(strings.TrimSpace(p)); p {
	case "":
		return ""
	case "plus", "pro", "team", "business", "enterprise", "free", "edu":
		return strings.ToUpper(p[:1]) + p[1:]
	}
	return p
}
