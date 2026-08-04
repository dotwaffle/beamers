package acceptance_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	frontendui "github.com/dotwaffle/beamers/internal/frontend"
)

func TestBrowserBuildsPrivateMyScheduleFromFavoriteSessions(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	sessionPath := "/events/beamconf-2099/schedule/sessions/" + strconv.FormatInt(sessionID, 10)
	favoritePath := "/schedule/sessions/" + strconv.FormatInt(sessionID, 10) + "/favorite"

	anonymous := authenticatedClient(t)
	anonymous.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	page := getFrontendPage(t, anonymous, server.publicAddress, sessionPath)
	if page.status != http.StatusOK ||
		!strings.Contains(page.body, "/sign-in?return_to=%2Fevents%2Fbeamconf-2099%2Fschedule%2Fsessions%2F") {
		t.Fatalf("anonymous Favorite invitation = %d %q", page.status, page.body)
	}

	const (
		accountName = "Favorite Attendee"
		password    = "favorite correct horse battery staple"
	)
	assertJSONRequest(
		t,
		administrator,
		server.address,
		"/admin/accounts",
		map[string]string{
			"name": accountName, "password": password,
			"command_id": "create-favorite-attendee",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Favorite Attendee\",\"administrator\":false}\n",
	)

	signInPage := getFrontendPage(
		t,
		anonymous,
		server.address,
		"/sign-in?return_to="+url.QueryEscape(sessionPath),
	)
	signIn := postFrontendForm(t, anonymous, server.address, "/sign-in", url.Values{
		"csrf_token": {requireFrontendCSRF(t, signInPage)},
		"handle":     {accountName},
		"password":   {password},
		"return_to":  {sessionPath},
	})
	if signIn.status != http.StatusSeeOther ||
		signIn.header.Get("Location") != sessionPath {
		t.Fatalf("Favorite sign-in return = %d Location %q", signIn.status, signIn.header.Get("Location"))
	}

	page = getFrontendPage(t, anonymous, server.address, sessionPath)
	if page.header.Get("Cache-Control") != "private, no-store" ||
		page.header.Get("ETag") != "" {
		t.Fatalf(
			"private Schedule cache headers = Cache-Control %q ETag %q",
			page.header.Get("Cache-Control"),
			page.header.Get("ETag"),
		)
	}
	if !strings.Contains(page.body, frontendui.HTMXPath) {
		t.Fatalf("Favorite Session page lacks htmx: %q", page.body)
	}
	add := postFrontendForm(
		t,
		anonymous,
		server.address,
		favoritePath,
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"favorite":   {"true"},
			"return_to":  {sessionPath},
		},
	)
	if add.status != http.StatusSeeOther || add.header.Get("Location") != sessionPath {
		t.Fatalf("no-JavaScript Favorite add = %d Location %q", add.status, add.header.Get("Location"))
	}

	page = getFrontendPage(t, anonymous, server.address, sessionPath)
	removeValues := url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"favorite":   {"false"},
		"return_to":  {sessionPath},
	}
	removeRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+server.address+favoritePath,
		strings.NewReader(removeValues.Encode()),
	)
	if err != nil {
		t.Fatalf("create Favorite removal request: %v", err)
	}
	removeRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	removeRequest.Header.Set("HX-Request", "true")
	removedLive := doFrontendRequest(t, anonymous, removeRequest)
	if removedLive.status != http.StatusOK ||
		removedLive.header.Get("HX-Refresh") != "" ||
		!strings.Contains(removedLive.body, "Add to My Schedule") {
		t.Fatalf(
			"live Favorite removal = %d HX-Refresh %q body %q",
			removedLive.status,
			removedLive.header.Get("HX-Refresh"),
			removedLive.body,
		)
	}

	page = getFrontendPage(t, anonymous, server.address, sessionPath)
	add = postFrontendForm(
		t,
		anonymous,
		server.address,
		favoritePath,
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"favorite":   {"true"},
			"return_to":  {sessionPath},
		},
	)
	if add.status != http.StatusSeeOther || add.header.Get("Location") != sessionPath {
		t.Fatalf("repeated no-JavaScript Favorite add = %d Location %q", add.status, add.header.Get("Location"))
	}

	page = getFrontendPage(t, anonymous, server.address, "/my-schedule")
	if page.status != http.StatusOK ||
		!strings.Contains(page.body, "Opening Keynote") ||
		!strings.Contains(page.body, "Remove from My Schedule") {
		t.Fatalf("My Schedule after Favorite = %d %q", page.status, page.body)
	}
	schedulePage := getFrontendPage(t, anonymous, server.address, "/events/beamconf-2099/schedule")
	if schedulePage.status != http.StatusOK ||
		!strings.Contains(schedulePage.body, "Opening Keynote") ||
		!strings.Contains(schedulePage.body, "Remove from My Schedule") {
		t.Fatalf("Schedule after Favorite = %d %q", schedulePage.status, schedulePage.body)
	}
	otherAccount := getFrontendPage(t, administrator, server.address, "/my-schedule")
	if otherAccount.status != http.StatusOK ||
		strings.Contains(otherAccount.body, "Opening Keynote") ||
		strings.Contains(otherAccount.body, accountName) {
		t.Fatalf("other Account inspected Favorites = %d %q", otherAccount.status, otherAccount.body)
	}
	public := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		"/events/beamconf-2099/schedule",
	)
	if !strings.HasPrefix(public.header.Get("Cache-Control"), "public,") ||
		public.header.Get("ETag") == "" {
		t.Fatalf(
			"public Schedule cache headers = Cache-Control %q ETag %q",
			public.header.Get("Cache-Control"),
			public.header.Get("ETag"),
		)
	}
	for _, private := range []string{accountName, "Favorite count", "endorsement", "notification"} {
		if strings.Contains(public.body, private) {
			t.Fatalf("public Schedule disclosed %q: %q", private, public.body)
		}
	}

	privateSession := postFrontendForm(
		t,
		anonymous,
		server.address,
		"/schedule/sessions/2/favorite",
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"favorite":   {"true"},
			"return_to":  {"/events/beamconf-2099/schedule/sessions/2"},
		},
	)
	if privateSession.status != http.StatusNotFound {
		t.Fatalf("Crew Only Favorite = %d %q", privateSession.status, privateSession.body)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	server = startBeamersWithPublicListener(t, bin, dataDir)
	page = getFrontendPage(t, anonymous, server.address, "/my-schedule")
	if page.status != http.StatusOK || !strings.Contains(page.body, "Opening Keynote") {
		t.Fatalf("restarted My Schedule = %d %q", page.status, page.body)
	}

	removed := postFrontendForm(
		t,
		anonymous,
		server.address,
		favoritePath,
		url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"favorite":   {"false"},
			"return_to":  {"/my-schedule"},
		},
	)
	if removed.status != http.StatusSeeOther ||
		removed.header.Get("Location") != "/my-schedule" {
		t.Fatalf("no-JavaScript Favorite removal = %d Location %q", removed.status, removed.header.Get("Location"))
	}
	page = getFrontendPage(t, anonymous, server.address, "/my-schedule")
	if page.status != http.StatusOK || strings.Contains(page.body, "Opening Keynote") {
		t.Fatalf("My Schedule after removal = %d %q", page.status, page.body)
	}

	for _, returnTo := range []string{"https://example.invalid/stolen", `/\evil.example`} {
		freshClient := authenticatedClient(t)
		freshClient.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		signInPage = getFrontendPage(
			t,
			freshClient,
			server.address,
			"/sign-in?return_to="+url.QueryEscape(returnTo),
		)
		signIn = postFrontendForm(t, freshClient, server.address, "/sign-in", url.Values{
			"csrf_token": {requireFrontendCSRF(t, signInPage)},
			"handle":     {accountName},
			"password":   {password},
			"return_to":  {returnTo},
		})
		if signIn.status != http.StatusSeeOther || signIn.header.Get("Location") != "/" {
			t.Fatalf(
				"external sign-in return %q = %d Location %q",
				returnTo,
				signIn.status,
				signIn.header.Get("Location"),
			)
		}
	}
	server.stop(t)
}
