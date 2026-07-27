package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/federation"
	"github.com/dotwaffle/beamers/internal/operations"
)

func TestSceneIDBrowserFlowNeverMergesMutableProfiles(t *testing.T) {
	var providerState struct {
		sync.Mutex
		subject       string
		displayName   string
		tokenRequests int
	}
	providerState.subject = "scene-subject-one"
	providerState.displayName = "Shared Mutable Name"
	const clientSecret = "host-only-scene-secret"
	provider := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/oauth/token/":
			clientID, secret, ok := request.BasicAuth()
			if !ok || clientID != "scene-client" || secret != clientSecret {
				t.Errorf("SceneID token authentication = %q/%q/%v", clientID, secret, ok)
				response.WriteHeader(http.StatusUnauthorized)
				return
			}
			providerState.Lock()
			providerState.tokenRequests++
			providerState.Unlock()
			writeServerJSON(t, response, map[string]any{
				"access_token": "scene-access", "token_type": "Bearer", "expires_in": 60,
			})
		case "/api/3.0/me/":
			if request.Header.Get("Authorization") != "Bearer scene-access" {
				response.WriteHeader(http.StatusUnauthorized)
				return
			}
			providerState.Lock()
			subject, displayName := providerState.subject, providerState.displayName
			providerState.Unlock()
			writeServerJSON(t, response, map[string]any{
				"success": true,
				"user": map[string]string{
					"id": subject, "display_name": displayName,
					"email": "same-mutable@example.test",
				},
			})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(provider.Close)
	providerURL, err := url.Parse(provider.URL)
	if err != nil {
		t.Fatalf("parse fake provider URL: %v", err)
	}
	sceneID, err := federation.NewSceneID(
		federation.Config{
			ClientID: "scene-client", ClientSecret: clientSecret,
			CallbackURL:          "http://127.0.0.1/auth/federation/sceneid/callback",
			AllowAccountCreation: true,
		},
		&http.Client{
			Timeout:   time.Second,
			Transport: rewriteSceneIDTransport{target: providerURL},
		},
	)
	if err != nil {
		t.Fatalf("create SceneID adapter: %v", err)
	}

	dataDir := t.TempDir()
	if err = operations.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize installation: %v", err)
	}
	installation, err := operations.OpenInstallation(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open installation: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := installation.Close(); closeErr != nil {
			t.Errorf("close installation: %v", closeErr)
		}
	})
	bootstrap, err := installation.Authentication().IssueBootstrap(t.Context())
	if err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	administrator, err := installation.Authentication().BootstrapAdministrator(
		t.Context(),
		bootstrap,
		"Ada Admin",
		"administrator password",
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}

	mux := newRouteMux()
	registerFederationRoutes(
		mux,
		installation.Authentication(),
		sceneID,
		slog.New(slog.DiscardHandler),
	)
	beamers := httptest.NewServer(protectInterfaces(mux, interfacePolicy{
		listenerAddress: &net.TCPAddr{IP: net.ParseIP("127.0.0.1")},
	}))
	t.Cleanup(beamers.Close)

	client := federationBrowserClient(t)
	providers := doFederationRequest(
		t,
		client,
		http.MethodGet,
		beamers.URL+"/auth/federation/providers",
		"",
	)
	body := readResponseBody(t, providers)
	_ = providers.Body.Close()
	if strings.Contains(body, clientSecret) ||
		strings.Contains(body, "scene-client") ||
		body != `[{"id":"sceneid","label":"SceneID","allow_account_creation":true}]`+"\n" {
		t.Fatalf("browser provider configuration = %q", body)
	}

	stateClient := federationBrowserClient(t)
	wrongState := beginSceneID(t, stateClient, beamers.URL, "", "")
	wrongStateResponse := finishSceneID(t, stateClient, beamers.URL, wrongState+"-wrong")
	if wrongStateResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong state callback status = %d", wrongStateResponse.StatusCode)
	}
	_ = wrongStateResponse.Body.Close()

	failedState := beginSceneID(t, client, beamers.URL, "", "")
	failed := doFederationRequest(
		t,
		client,
		http.MethodGet,
		beamers.URL+"/auth/federation/sceneid/callback?state="+
			url.QueryEscape(failedState)+"&error=access_denied",
		"",
	)
	if failed.StatusCode != http.StatusUnauthorized {
		t.Fatalf("provider failure callback status = %d", failed.StatusCode)
	}
	_ = failed.Body.Close()

	firstState := beginSceneID(t, client, beamers.URL, "", "")
	first := finishSceneID(t, client, beamers.URL, firstState)
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("first SceneID callback status = %d", first.StatusCode)
	}
	_ = first.Body.Close()
	firstAccount := authenticatedFederationAccount(t, installation, client, beamers.URL)
	if firstAccount.Name != "Shared Mutable Name" {
		t.Fatalf("first federated Account = %+v", firstAccount)
	}

	providerState.Lock()
	providerState.displayName = "Changed Mutable Name"
	providerState.Unlock()
	secondClient := federationBrowserClient(t)
	secondState := beginSceneID(t, secondClient, beamers.URL, "", "")
	second := finishSceneID(t, secondClient, beamers.URL, secondState)
	_ = second.Body.Close()
	secondAccount := authenticatedFederationAccount(t, installation, secondClient, beamers.URL)
	if secondAccount.ID != firstAccount.ID || secondAccount.Name != firstAccount.Name {
		t.Fatalf("changed profile remapped Account: %+v then %+v", firstAccount, secondAccount)
	}

	providerState.Lock()
	providerState.subject = "scene-subject-two"
	providerState.displayName = "Changed Mutable Name"
	providerState.Unlock()
	thirdClient := federationBrowserClient(t)
	thirdState := beginSceneID(t, thirdClient, beamers.URL, "", "")
	third := finishSceneID(t, thirdClient, beamers.URL, thirdState)
	_ = third.Body.Close()
	thirdAccount := authenticatedFederationAccount(t, installation, thirdClient, beamers.URL)
	if thirdAccount.ID == firstAccount.ID {
		t.Fatalf("distinct SceneID subjects merged Account %d", firstAccount.ID)
	}

	replay := finishSceneID(t, thirdClient, beamers.URL, thirdState)
	if replay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed callback status = %d", replay.StatusCode)
	}
	_ = replay.Body.Close()
	providerState.Lock()
	tokenRequests := providerState.tokenRequests
	providerState.Unlock()
	if tokenRequests != 3 {
		t.Fatalf("provider token requests after replay = %d, want 3", tokenRequests)
	}

	linkClient := federationBrowserClient(t)
	beamersURL, _ := url.Parse(beamers.URL)
	linkClient.Jar.SetCookies(beamersURL, []*http.Cookie{{
		Name: sessionCookieName, Value: administrator.Token, Path: "/",
	}})
	csrf := base64.RawURLEncoding.EncodeToString(make([]byte, csrfTokenBytes))
	linkClient.Jar.SetCookies(beamersURL, []*http.Cookie{{
		Name: csrfCookieName, Value: csrf, Path: "/",
	}})
	providerState.Lock()
	providerState.subject = "linked-administrator"
	providerState.Unlock()
	linkState := beginSceneID(t, linkClient, beamers.URL, "link", csrf)
	linked := finishSceneID(t, linkClient, beamers.URL, linkState)
	_ = linked.Body.Close()
	linkedSignIn := federationBrowserClient(t)
	signInState := beginSceneID(t, linkedSignIn, beamers.URL, "", "")
	signedIn := finishSceneID(t, linkedSignIn, beamers.URL, signInState)
	_ = signedIn.Body.Close()
	linkedAccount := authenticatedFederationAccount(t, installation, linkedSignIn, beamers.URL)
	if linkedAccount.ID != administrator.Account.ID {
		t.Fatalf("linked SceneID Account = %+v, want Administrator", linkedAccount)
	}
}

type rewriteSceneIDTransport struct {
	target *url.URL
}

func (transport rewriteSceneIDTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = transport.target.Scheme
	clone.URL.Host = transport.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func federationBrowserClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create federation cookie jar: %v", err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func beginSceneID(
	t *testing.T,
	client *http.Client,
	baseURL string,
	mode string,
	csrf string,
) string {
	t.Helper()
	method := http.MethodGet
	target := baseURL + "/auth/federation/sceneid/begin"
	body := ""
	if mode != "" {
		method = http.MethodPost
		body = url.Values{"mode": {mode}, "csrf_token": {csrf}}.Encode()
	}
	response := doFederationRequest(t, client, method, target, body)
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close SceneID begin response: %v", err)
		}
	}()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("begin SceneID status = %d: %s", response.StatusCode, readResponseBody(t, response))
	}
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil || location.Host != "id.scene.org" {
		t.Fatalf("SceneID authorization location = %q, %v", response.Header.Get("Location"), err)
	}
	if location.Query().Get("redirect_uri") !=
		"http://127.0.0.1/auth/federation/sceneid/callback" {
		t.Fatalf("SceneID redirect URI = %q", location.Query().Get("redirect_uri"))
	}
	return location.Query().Get("state")
}

func finishSceneID(
	t *testing.T,
	client *http.Client,
	baseURL string,
	state string,
) *http.Response {
	t.Helper()
	return doFederationRequest(
		t,
		client,
		http.MethodGet,
		baseURL+"/auth/federation/sceneid/callback?state="+url.QueryEscape(state)+"&code=good-code",
		"",
	)
}

func doFederationRequest(
	t *testing.T,
	client *http.Client,
	method string,
	target string,
	body string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatalf("create federation request: %v", err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", strings.TrimSuffix(target, "/auth/federation/sceneid/begin"))
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send federation request: %v", err)
	}
	return response
}

func authenticatedFederationAccount(
	t *testing.T,
	installation *operations.Installation,
	client *http.Client,
	baseURL string,
) auth.Account {
	t.Helper()
	found, _ := url.Parse(baseURL)
	for _, cookie := range client.Jar.Cookies(found) {
		if cookie.Name == sessionCookieName {
			account, err := installation.Authentication().Authenticate(t.Context(), cookie.Value)
			if err != nil {
				t.Fatalf("authenticate federation session: %v", err)
			}
			return account
		}
	}
	t.Fatal("federation session cookie missing")
	return auth.Account{}
}

func readResponseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read federation response: %v", err)
	}
	return string(body)
}

func writeServerJSON(t *testing.T, response http.ResponseWriter, value any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Errorf("encode fake SceneID response: %v", err)
	}
}
