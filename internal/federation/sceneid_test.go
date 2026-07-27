package federation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestSceneIDAdapterUsesImmutableUserID(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			clientID, clientSecret, ok := request.BasicAuth()
			if !ok || clientID != "scene-client" || clientSecret != "scene-secret" {
				t.Errorf("SceneID token authentication = %q/%q/%v", clientID, clientSecret, ok)
				response.WriteHeader(http.StatusUnauthorized)
				return
			}
			if err := request.ParseForm(); err != nil ||
				request.Form.Get("code") != "scene-code" {
				t.Errorf("SceneID token form = %#v, %v", request.Form, err)
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			writeFederationJSON(t, response, map[string]any{
				"access_token": "scene-access", "token_type": "Bearer", "expires_in": 60,
			})
		case "/me":
			if request.Header.Get("Authorization") != "Bearer scene-access" {
				t.Errorf("SceneID profile authorization = %q", request.Header.Get("Authorization"))
				response.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeFederationJSON(t, response, map[string]any{
				"success": true,
				"user": map[string]string{
					"id": "42", "display_name": "Mutable Scener",
				},
			})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(provider.Close)

	adapter, err := newSceneID(
		Config{
			ClientID: "scene-client", ClientSecret: "scene-secret",
			CallbackURL:          "http://localhost/auth/federation/sceneid/callback",
			AllowAccountCreation: true,
		},
		endpoints{
			AuthorizationURL: provider.URL + "/authorize",
			TokenURL:         provider.URL + "/token",
			ProfileURL:       provider.URL + "/me",
		},
		provider.Client(),
	)
	if err != nil {
		t.Fatalf("create SceneID adapter: %v", err)
	}
	authorizationURL, err := adapter.AuthorizationURL(Authorization{
		State: "scene-state",
	})
	if err != nil {
		t.Fatalf("create SceneID authorization: %v", err)
	}
	authorization, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatalf("parse SceneID authorization: %v", err)
	}
	if authorization.Query().Get("state") != "scene-state" ||
		authorization.Query().Get("scope") != "basic" ||
		authorization.Query().Get("client_secret") != "" {
		t.Errorf("SceneID authorization URL = %s", authorization)
	}
	identity, err := adapter.Identity(t.Context(), Callback{
		Code: "scene-code",
	})
	if err != nil {
		t.Fatalf("resolve SceneID identity: %v", err)
	}
	if identity != (Identity{Subject: "42", DisplayName: "Mutable Scener"}) {
		t.Errorf("SceneID identity = %+v", identity)
	}
}

func TestSceneIDRequiresHTTPSOutsideLoopback(t *testing.T) {
	_, err := newSceneID(
		Config{
			ClientID: "client", ClientSecret: "secret",
			CallbackURL: "http://beamers.example/auth/federation/sceneid/callback",
		},
		endpoints{
			AuthorizationURL: "https://id.scene.org/oauth/authorize/",
			TokenURL:         "https://id.scene.org/oauth/token/",
			ProfileURL:       "https://id.scene.org/api/3.0/me/",
		},
		http.DefaultClient,
	)
	if err == nil {
		t.Fatal("non-loopback HTTP callback accepted")
	}
	if _, err = newSceneID(
		Config{
			ClientID: "client", ClientSecret: "secret",
			CallbackURL: "http://127.0.0.1/auth/federation/sceneid/callback",
		},
		endpoints{
			AuthorizationURL: "https://id.scene.org/oauth/authorize/",
			TokenURL:         "https://id.scene.org/oauth/token/",
			ProfileURL:       "https://id.scene.org/api/3.0/me/",
		},
		http.DefaultClient,
	); err != nil {
		t.Fatalf("loopback HTTP callback rejected: %v", err)
	}
}

func TestSceneIDIdentityFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "token rejection",
			handler: func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(http.StatusUnauthorized)
			},
		},
		{
			name: "malformed profile",
			handler: func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/token" {
					writeFederationJSON(t, response, map[string]any{
						"access_token": "access", "token_type": "Bearer",
					})
					return
				}
				_, _ = response.Write([]byte("{"))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := httptest.NewServer(test.handler)
			t.Cleanup(provider.Close)
			adapter, err := newSceneID(
				Config{
					ClientID: "client", ClientSecret: "secret",
					CallbackURL: "http://localhost/auth/federation/sceneid/callback",
				},
				endpoints{
					AuthorizationURL: provider.URL + "/authorize",
					TokenURL:         provider.URL + "/token",
					ProfileURL:       provider.URL + "/me",
				},
				provider.Client(),
			)
			if err != nil {
				t.Fatalf("create SceneID adapter: %v", err)
			}
			if _, err = adapter.Identity(t.Context(), Callback{Code: "code"}); err == nil {
				t.Fatal("invalid SceneID response accepted")
			}
		})
	}
}

func writeFederationJSON(t *testing.T, response http.ResponseWriter, value any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Errorf("encode fake provider response: %v", err)
	}
}
