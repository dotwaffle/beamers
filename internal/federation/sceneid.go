package federation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
)

const maxProfileBytes = 64 << 10

const (
	sceneIDAuthorizationURL = "https://id.scene.org/oauth/authorize/"
	sceneIDTokenURL         = "https://id.scene.org/oauth/token/" //nolint:gosec // Public OAuth endpoint.
	sceneIDProfileURL       = "https://id.scene.org/api/3.0/me/"
)

type endpoints struct {
	AuthorizationURL string
	TokenURL         string
	ProfileURL       string
}

// SceneID authenticates one configured SceneID OAuth client.
type SceneID struct {
	config    Config
	endpoints endpoints
	client    *http.Client
}

// NewSceneID creates the constrained SceneID OAuth adapter.
func NewSceneID(config Config, client *http.Client) (*SceneID, error) {
	return newSceneID(config, endpoints{
		AuthorizationURL: sceneIDAuthorizationURL,
		TokenURL:         sceneIDTokenURL,
		ProfileURL:       sceneIDProfileURL,
	}, client)
}

func newSceneID(
	config Config,
	endpoints endpoints,
	client *http.Client,
) (*SceneID, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	for _, endpoint := range []string{
		endpoints.AuthorizationURL,
		endpoints.TokenURL,
		endpoints.ProfileURL,
	} {
		if err := validateHTTPSURL(endpoint); err != nil {
			return nil, err
		}
	}
	if client == nil {
		return nil, errors.New("SceneID HTTP client is required")
	}
	return &SceneID{config: config, endpoints: endpoints, client: client}, nil
}

// Public returns the safe browser description of SceneID.
func (adapter *SceneID) Public() PublicProvider {
	return publicProvider(adapter.config.AllowAccountCreation)
}

// AuthorizationURL returns a state-bound SceneID authorization endpoint.
func (adapter *SceneID) AuthorizationURL(input Authorization) (string, error) {
	if err := validateAuthorization(input); err != nil {
		return "", err
	}
	config := adapter.oauthConfig()
	return config.AuthCodeURL(input.State), nil
}

// Identity exchanges an authorization code and returns SceneID's immutable user ID.
func (adapter *SceneID) Identity(
	ctx context.Context,
	input Callback,
) (Identity, error) {
	if input.Code == "" {
		return Identity{}, errors.New("SceneID callback code is required")
	}
	config := adapter.oauthConfig()
	tokenContext := context.WithValue(ctx, oauth2.HTTPClient, adapter.client)
	token, err := config.Exchange(tokenContext, input.Code)
	if err != nil {
		return Identity{}, errors.New("exchange SceneID authorization")
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		adapter.endpoints.ProfileURL,
		http.NoBody,
	)
	if err != nil {
		return Identity{}, errors.New("create SceneID profile request")
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	client := *adapter.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return Identity{}, errors.New("request SceneID profile")
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return Identity{}, errors.New("SceneID profile request failed")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProfileBytes+1))
	if err != nil || len(body) > maxProfileBytes {
		return Identity{}, errors.New("read SceneID profile")
	}
	var profile struct {
		Success bool `json:"success"`
		User    struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"user"`
	}
	if err = json.Unmarshal(body, &profile); err != nil {
		return Identity{}, errors.New("decode SceneID profile")
	}
	subject := strings.TrimSpace(profile.User.ID)
	if !profile.Success || subject == "" || len(subject) > 1024 {
		return Identity{}, errors.New("SceneID subject is invalid")
	}
	return Identity{Subject: subject, DisplayName: profile.User.DisplayName}, nil
}

func (adapter *SceneID) oauthConfig() oauth2.Config {
	return oauth2.Config{
		ClientID: adapter.config.ClientID, ClientSecret: adapter.config.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL: adapter.endpoints.AuthorizationURL, TokenURL: adapter.endpoints.TokenURL,
			AuthStyle: oauth2.AuthStyleInHeader,
		},
		RedirectURL: adapter.config.CallbackURL,
		Scopes:      []string{"basic"},
	}
}
