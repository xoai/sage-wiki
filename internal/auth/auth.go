package auth

import (
	"fmt"
	"time"
)

type FlowType int

const (
	FlowPKCE       FlowType = iota
	FlowDeviceCode          // reserved for future GitHub Copilot login
	FlowImportOnly
)

type Credential struct {
	Provider     string `json:"-"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	AccountID    string `json:"account_id,omitempty"`
	Source       string `json:"source"`
}

func (c *Credential) ExpiresWithin(d time.Duration) bool {
	return time.Until(time.Unix(c.ExpiresAt, 0)) < d
}

func (c *Credential) ExtraHeaders() map[string]string {
	if c.Provider == "openai" && c.AccountID != "" {
		return map[string]string{"ChatGPT-Account-ID": c.AccountID}
	}
	return nil
}

func (c *Credential) String() string {
	masked := "****"
	if len(c.AccessToken) >= 4 {
		masked += c.AccessToken[len(c.AccessToken)-4:]
	} else if len(c.AccessToken) > 0 {
		masked += c.AccessToken
	}
	return fmt.Sprintf("%s:%s", c.Provider, masked)
}

type ProviderConfig struct {
	AuthorizeURL    string
	TokenURL        string
	ClientID        string
	RedirectPort    int
	RedirectPath    string
	Scopes          []string
	ExtraAuthParams map[string]string
	FlowType        FlowType
	ImportPath      string
	AccountIDClaim  string
}
