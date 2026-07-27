// Package federation authenticates Accounts through configured identity providers.
package federation

import (
	"errors"
	"net"
	"net/url"
)

const sceneIDCallbackPath = "/auth/federation/sceneid/callback"

// Config is host-managed SceneID configuration.
type Config struct {
	ClientID             string `json:"client_id"`
	ClientSecret         string `json:"client_secret"`
	CallbackURL          string `json:"callback_url"`
	AllowAccountCreation bool   `json:"allow_account_creation"`
}

// PublicProvider is the non-secret browser description of configured SceneID.
type PublicProvider struct {
	ID                   string `json:"id"`
	Label                string `json:"label"`
	AllowAccountCreation bool   `json:"allow_account_creation"`
}

// Authorization binds an authorization request to one browser flow.
type Authorization struct {
	State string
}

// Callback contains one provider authorization response and its server-held proof.
type Callback struct {
	Code string
}

// Identity contains one immutable SceneID subject and its mutable display name.
type Identity struct {
	Subject     string
	DisplayName string
}

func validateConfig(config Config) error {
	switch {
	case config.ClientID == "":
		return errors.New("SceneID client ID is required")
	case config.ClientSecret == "":
		return errors.New("SceneID client secret is required")
	case validateCallbackURL(config.CallbackURL) != nil:
		return errors.New("SceneID callback URL must use the fixed callback path and HTTPS or loopback")
	default:
		return nil
	}
}

func validateCallbackURL(value string) error {
	if err := validateHTTPSURL(value); err != nil {
		return err
	}
	found, _ := url.Parse(value)
	if found.Path != sceneIDCallbackPath || found.RawQuery != "" {
		return errors.New("SceneID callback URL is invalid")
	}
	return nil
}

func validateAuthorization(input Authorization) error {
	if input.State == "" {
		return errors.New("SceneID state is required")
	}
	return nil
}

func validateHTTPSURL(value string) error {
	found, err := url.Parse(value)
	if err != nil || found.User != nil || found.Host == "" || found.Fragment != "" {
		return errors.New("federation URL is invalid")
	}
	if found.Scheme == "https" {
		return nil
	}
	host := found.Hostname()
	if found.Scheme == "http" && (host == "localhost" || net.ParseIP(host).IsLoopback()) {
		return nil
	}
	return errors.New("federation URL requires HTTPS")
}

func publicProvider(allowAccountCreation bool) PublicProvider {
	return PublicProvider{
		ID: "sceneid", Label: "SceneID",
		AllowAccountCreation: allowAccountCreation,
	}
}
