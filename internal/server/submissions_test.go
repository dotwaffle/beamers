package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/rundown"
)

func TestAccountSubmissionBrowserFlowKeepsEntriesPresentationsAndFilesPrivate(t *testing.T) {
	application, administratorToken, installation, _ := newDiagnosticsTestApplication(
		t,
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
	)
	administrator, err := installation.Authentication().Authenticate(
		t.Context(),
		administratorToken,
	)
	if err != nil {
		t.Fatalf("authenticate Administrator: %v", err)
	}
	now := time.Now().UTC()
	event, err := installation.Events().Create(
		t.Context(),
		administrator,
		events.CreateInput{
			Name: "Submission Event", PlannedStartDate: now.Format(time.DateOnly),
			PlannedEndDate: now.AddDate(0, 0, 1).Format(time.DateOnly),
			Timezone:       "UTC", EventLocale: "en-US", ContentLanguage: "en",
			EventDayBoundary: "06:00", EntryDefaultDisposition: "Pending",
			SubmissionEligibility: "AllAccounts",
			CommandID:             "submission-create-event",
		},
	)
	if err != nil {
		t.Fatalf("create submission Event: %v", err)
	}
	if _, err = installation.Events().GrantEventAccess(
		t.Context(),
		administrator,
		event.ID,
		administrator.ID,
		"Producer",
		"submission-grant-producer",
	); err != nil {
		t.Fatalf("grant submission Producer: %v", err)
	}
	administrator, err = installation.Authentication().Authenticate(
		t.Context(),
		administratorToken,
	)
	if err != nil {
		t.Fatalf("refresh submission Producer: %v", err)
	}
	event, err = installation.Events().Update(
		t.Context(),
		administrator,
		event.ID,
		events.CreateInput{
			Name: event.Name, Public: true, PublicSlug: event.PublicSlug,
			PlannedStartDate: event.PlannedStartDate, PlannedEndDate: event.PlannedEndDate,
			Timezone: event.Timezone, EventLocale: event.EventLocale,
			ContentLanguage: event.ContentLanguage, EventDayBoundary: event.EventDayBoundary,
			EntryDefaultDisposition:        event.EntryDefaultDisposition,
			SubmissionEligibility:          event.SubmissionEligibility,
			TargetAdjustmentPresetsSeconds: event.TargetAdjustmentPresetsSeconds,
			CommandID:                      "submission-publish-event", ExpectedRevision: event.Revision,
		},
	)
	if err != nil {
		t.Fatalf("publish submission Event: %v", err)
	}
	edit := capacityDraft(event.ID, now, capacityEnvelope{Locations: 2, Lanes: 2})
	edit.CommandID = "submission-stage-rundown"
	if _, err = installation.RundownCommands().EditDraft(
		t.Context(),
		administrator,
		edit,
	); err != nil {
		t.Fatalf("stage submission Rundown: %v", err)
	}
	preview, err := installation.RundownQueries().PublishPreview(
		t.Context(),
		administrator,
		rundown.PublishPreviewInput{EventID: event.ID},
	)
	if err != nil || len(preview.ValidationFailures) != 0 {
		t.Fatalf("preview submission Rundown = %+v, %v", preview, err)
	}
	if _, err = installation.RundownCommands().Publish(
		t.Context(),
		administrator,
		rundown.PublishInput{
			EventID: event.ID, CommandID: "submission-publish-rundown",
			Confirmation: rundown.PublishConfirmation{
				DraftRevision:     preview.DraftRevision,
				PublishedRevision: preview.PublishedRevision,
				ChangeIDs:         preview.ChangeIDs, Fingerprint: preview.Fingerprint,
			},
		},
	); err != nil {
		t.Fatalf("publish submission Rundown: %v", err)
	}
	published, err := installation.RundownQueries().CrewRundown(
		t.Context(),
		administrator,
		event.ID,
	)
	if err != nil {
		t.Fatalf("load submission Rundown: %v", err)
	}
	competitionID, presentationID := 0, 0
	for _, session := range published.Sessions {
		switch session.Type {
		case rundown.SessionCompetition:
			competitionID = session.ID
		case rundown.SessionPresentation:
			presentationID = session.ID
		default:
		}
	}
	if competitionID == 0 || presentationID == 0 {
		t.Fatal("published submission Competition or Presentation missing")
	}
	const attendeePassword = "attendee correct horse battery staple"
	attendee, err := installation.Authentication().CreateAccountWithDisplayName(
		t.Context(),
		administrator,
		"attendee",
		"Attendee",
		attendeePassword,
		"submission-create-attendee",
	)
	if err != nil {
		t.Fatalf("create attendee Account: %v", err)
	}
	other, err := installation.Authentication().CreateAccountWithDisplayName(
		t.Context(),
		administrator,
		"other",
		"Other",
		attendeePassword,
		"submission-create-other",
	)
	if err != nil {
		t.Fatalf("create other Account: %v", err)
	}
	attendeeSession, err := installation.Authentication().SignIn(
		t.Context(),
		attendee.Handle,
		attendeePassword,
	)
	if err != nil {
		t.Fatalf("sign in attendee: %v", err)
	}
	otherSession, err := installation.Authentication().SignIn(
		t.Context(),
		other.Handle,
		attendeePassword,
	)
	if err != nil {
		t.Fatalf("sign in other Account: %v", err)
	}

	beamers := httptestServer(t, application)
	client := submissionBrowserClient(t, beamers.URL, attendeeSession.Token)
	csrf := submissionCSRF(t, client, beamers.URL)
	administratorClient := submissionBrowserClient(t, beamers.URL, administratorToken)
	administratorCSRF := submissionCSRF(t, administratorClient, beamers.URL)
	presentationPath := fmt.Sprintf(
		"%s/backstage/events/%d/presentations/%d/submission",
		beamers.URL,
		event.ID,
		presentationID,
	)
	assignedResponse := submissionForm(
		t,
		administratorClient,
		presentationPath,
		url.Values{
			"csrf_token": {administratorCSRF}, "action": {"assign-submitter"},
			"command_id":        {"assign-presentation-attendee"},
			"expected_revision": {"0"}, "account_id": {strconv.Itoa(attendee.ID)},
		},
	)
	_ = assignedResponse.Body.Close()
	if assignedResponse.StatusCode != http.StatusOK {
		t.Fatalf("assign Presentation Submitter status = %d", assignedResponse.StatusCode)
	}
	otherClient := submissionBrowserClient(t, beamers.URL, otherSession.Token)
	if otherBody := submissionGet(t, otherClient, beamers.URL+"/submissions"); strings.Contains(
		otherBody,
		"Capacity Stage",
	) {
		t.Fatalf("unassigned Account saw private Presentation submission: %q", otherBody)
	}
	updatedPresentation := submissionForm(
		t,
		client,
		beamers.URL+"/submissions",
		url.Values{
			"csrf_token": {csrf}, "action": {"update-presentation"},
			"command_id": {"update-presentation-details"},
			"event_id":   {strconv.Itoa(event.ID)}, "session_id": {strconv.Itoa(presentationID)},
			"expected_revision": {"1"}, "speaker": {"Attendee Stage Credit"},
			"public_details": {"Attendee-approved details"},
		},
	)
	updatedPresentationBody := readResponseBody(t, updatedPresentation)
	_ = updatedPresentation.Body.Close()
	if updatedPresentation.StatusCode != http.StatusOK ||
		!strings.Contains(updatedPresentationBody, "Attendee Stage Credit") ||
		!strings.Contains(updatedPresentationBody, "Attendee-approved details") {
		t.Fatalf(
			"updated Presentation submission status/body = %d/%q",
			updatedPresentation.StatusCode,
			updatedPresentationBody,
		)
	}
	for index, content := range [][]byte{[]byte("first deck"), []byte("second deck")} {
		upload := submissionUpload(
			t,
			client,
			fmt.Sprintf(
				"%s/submissions/%d/presentations/%d/upload",
				beamers.URL,
				event.ID,
				presentationID,
			),
			csrf,
			fmt.Sprintf("upload-presentation-%d", index+1),
			fmt.Sprintf("slides-%d.pdf", index+1),
			"slides",
			content,
		)
		body := readResponseBody(t, upload)
		_ = upload.Body.Close()
		if upload.StatusCode != http.StatusOK ||
			!strings.Contains(body, fmt.Sprintf("slides-%d.pdf", index+1)) {
			t.Fatalf("Presentation upload %d status/body = %d/%q", index+1, upload.StatusCode, body)
		}
	}
	replacedResponse := submissionForm(
		t,
		administratorClient,
		presentationPath,
		url.Values{
			"csrf_token": {administratorCSRF}, "action": {"assign-submitter"},
			"command_id":        {"replace-presentation-attendee"},
			"expected_revision": {"2"}, "account_id": {strconv.Itoa(other.ID)},
		},
	)
	_ = replacedResponse.Body.Close()
	if replacedResponse.StatusCode != http.StatusOK {
		t.Fatalf("replace Presentation Submitter status = %d", replacedResponse.StatusCode)
	}
	if attendeeBody := submissionGet(t, client, beamers.URL+"/submissions"); strings.Contains(
		attendeeBody,
		"Capacity Stage",
	) {
		t.Fatalf("replaced Account retained Presentation access: %q", attendeeBody)
	}
	otherPresentationBody := submissionGet(t, otherClient, beamers.URL+"/submissions")
	if !strings.Contains(otherPresentationBody, "Attendee Stage Credit") ||
		!strings.Contains(otherPresentationBody, "slides-1.pdf") ||
		!strings.Contains(otherPresentationBody, "slides-2.pdf") {
		t.Fatalf("replacement Account Presentation projection = %q", otherPresentationBody)
	}

	response := submissionForm(
		t,
		client,
		beamers.URL+"/submissions",
		url.Values{
			"csrf_token": {csrf}, "action": {"create"},
			"command_id": {"submission-create-entry"},
			"event_id":   {strconv.Itoa(event.ID)}, "session_id": {strconv.Itoa(competitionID)},
			"entry_name": {"Private draft name"}, "public_details": {"Attendee details"},
		},
	)
	body := readResponseBody(t, response)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(body, "Private draft name") ||
		strings.Contains(body, "Crew notes") {
		t.Fatalf("created submission page status/body = %d/%q", response.StatusCode, body)
	}
	submissions, err := installation.Competition().Submissions(t.Context(), attendee)
	if err != nil || len(submissions) != 1 || len(submissions[0].Entries) != 1 {
		t.Fatalf("created Account submissions = %+v, %v", submissions, err)
	}
	entryID := submissions[0].Entries[0].ID
	content := []byte("immutable attendee archive")
	upload := submissionUpload(
		t,
		client,
		fmt.Sprintf(
			"%s/submissions/%d/entries/%d/upload",
			beamers.URL,
			event.ID,
			entryID,
		),
		csrf,
		"submission-upload-entry",
		"entry.zip",
		"archive",
		content,
	)
	uploadBody := readResponseBody(t, upload)
	_ = upload.Body.Close()
	digest := sha256.Sum256(content)
	if upload.StatusCode != http.StatusOK ||
		!strings.Contains(uploadBody, "entry.zip") ||
		!strings.Contains(uploadBody, hex.EncodeToString(digest[:])) {
		t.Fatalf("uploaded submission page status/body = %d/%q", upload.StatusCode, uploadBody)
	}

	otherRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		beamers.URL+"/submissions",
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create other Account submission request: %v", err)
	}
	otherResponse, err := otherClient.Do(otherRequest)
	if err != nil {
		t.Fatalf("read other Account submissions: %v", err)
	}
	otherBody := readResponseBody(t, otherResponse)
	_ = otherResponse.Body.Close()
	if strings.Contains(otherBody, "Private draft name") ||
		strings.Contains(otherBody, "entry.zip") ||
		strings.Contains(otherBody, hex.EncodeToString(digest[:])) {
		t.Fatalf("other Account saw private submission: %q", otherBody)
	}
	foreignUpload, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		fmt.Sprintf(
			"%s/submissions/%d/entries/%d/upload",
			beamers.URL,
			event.ID,
			entryID,
		),
		strings.NewReader("not a multipart body"),
	)
	if err != nil {
		t.Fatalf("create foreign Account upload: %v", err)
	}
	foreignUpload.Header.Set("Content-Type", "multipart/form-data; boundary=missing")
	foreignUpload.Header.Set("Origin", beamers.URL)
	foreignResponse, err := otherClient.Do(foreignUpload)
	if err != nil {
		t.Fatalf("reject foreign Account upload: %v", err)
	}
	_ = foreignResponse.Body.Close()
	if foreignResponse.StatusCode != http.StatusNotFound {
		t.Fatalf(
			"foreign Account upload = %d, want ownership rejection before multipart parsing",
			foreignResponse.StatusCode,
		)
	}
}

func httptestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func submissionBrowserClient(
	t *testing.T,
	baseURL, token string,
) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create submission cookie jar: %v", err)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse submission server URL: %v", err)
	}
	jar.SetCookies(parsed, []*http.Cookie{{
		Name: sessionCookieName, Value: token, Path: "/",
	}})
	return &http.Client{Jar: jar, Timeout: 5 * time.Second}
}

func submissionCSRF(t *testing.T, client *http.Client, baseURL string) string {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		baseURL+"/submissions",
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("create submission page request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("load submission page: %v", err)
	}
	_ = response.Body.Close()
	parsed, _ := url.Parse(baseURL)
	for _, cookie := range client.Jar.Cookies(parsed) {
		if cookie.Name == csrfCookieName {
			return cookie.Value
		}
	}
	t.Fatal("submission CSRF cookie missing")
	return ""
}

func submissionGet(t *testing.T, client *http.Client, target string) string {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Fatalf("create submission GET request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("load submission page: %v", err)
	}
	body := readResponseBody(t, response)
	_ = response.Body.Close()
	return body
}

func submissionForm(
	t *testing.T,
	client *http.Client,
	target string,
	values url.Values,
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		target,
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		t.Fatalf("create Competition Entry form request: %v", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("submit Competition Entry form: %v", err)
	}
	return response
}

func submissionUpload(
	t *testing.T,
	client *http.Client,
	target, csrf string,
	commandID, filename, name string,
	content []byte,
) *http.Response {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"csrf_token": csrf, "command_id": commandID, "name": name,
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write upload field: %v", err)
		}
	}
	file, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create upload file: %v", err)
	}
	if _, err = file.Write(content); err != nil {
		t.Fatalf("write upload file: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("close upload form: %v", err)
	}
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		target,
		&body,
	)
	if err != nil {
		t.Fatalf("create upload request: %v", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("upload Competition Entry file: %v", err)
	}
	return response
}
