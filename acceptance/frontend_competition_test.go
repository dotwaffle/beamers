package acceptance_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	"github.com/dotwaffle/beamers/gen/beamers/program/v1/programv1connect"
	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/gen/beamers/rundown/v1/rundownv1connect"
)

func TestBrowserManagesCompetitionEntries(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	path := "/backstage/events/1/competitions/" +
		strconv.FormatInt(competitionID, 10) + "/entries"

	page := getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Competition Entries and Attachments",
		`<html lang="en-GB" data-locale="en-GB">`,
		`href="/backstage/events/1/sessions"`,
		"Submission Deadline",
		"2099-08-21 13:30 CEST",
		`src="/assets/event-time.js"`,
		"Start preflight",
		`name="entry_name"`,
	} {
		if page.status != http.StatusOK || !strings.Contains(page.body, want) {
			t.Fatalf("Competition Entries page lacks %q: %d %q", want, page.status, page.body)
		}
	}
	planning := getFrontendPage(
		t,
		administrator,
		server.address,
		"/backstage/events/1/planning",
	)
	if !strings.Contains(planning.body, `href="`+path+`"`) ||
		!strings.Contains(planning.body, "Manage Demo Competition") {
		t.Fatalf("published Competition lacks Entries route: %d %q", planning.status, planning.body)
	}
	unscopedOperator := provisionOperatorWithLanes(t, administrator, server, nil)
	if denied := getFrontendPage(
		t, unscopedOperator, server.address, path,
	); denied.status != http.StatusNotFound {
		t.Fatalf("unscoped Operator Competition Entries = %d %q", denied.status, denied.body)
	}
	if denied := postFrontendForm(
		t,
		unscopedOperator,
		server.address,
		path,
		url.Values{
			"action":            {"close-reopen-window"},
			"window_id":         {"1"},
			"expected_revision": {"1"},
			"confirm_close":     {"true"},
		},
	); denied.status != http.StatusNotFound {
		t.Fatalf("unscoped Operator Reopen Window update = %d %q", denied.status, denied.body)
	}

	invalidEntryDetails := strings.Repeat("d", 10001)
	invalidEntry := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, page)},
		"action":         {"create-entry"},
		"command_id":     {"browser-invalid-entry"},
		"entry_name":     {""},
		"public_details": {invalidEntryDetails},
		"crew_notes":     {"Safe Crew note"},
	})
	if invalidEntry.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidEntry.body, invalidEntryDetails) {
		t.Fatalf("invalid Entry creation = %d %q", invalidEntry.status, invalidEntry.body)
	}
	assertAccessibleFormErrors(t, invalidEntry, map[string]string{
		"create-entry-entry-name":     "Enter an Entry name.",
		"create-entry-public-details": "Enter no more than 10000 characters.",
	})

	created := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, invalidEntry)},
		"action":         {"create-entry"},
		"command_id":     {"browser-create-entry"},
		"entry_name":     {"Project Aurora"},
		"public_details": {"A public abstract"},
		"crew_notes":     {"Crew-only staging note"},
	})
	if created.status != http.StatusSeeOther || created.header.Get("Location") != path {
		t.Fatalf("browser Entry creation = %d %q", created.status, created.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"Project Aurora",
		"A public abstract",
		"Crew-only staging note",
		`name="action" value="review-entry"`,
		`name="action" value="change-disposition"`,
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("created Entry page lacks %q: %q", want, page.body)
		}
	}
	if !strings.Contains(page.body, "missing_file_delivery") ||
		!strings.Contains(page.body, `role="alert"`) {
		t.Fatalf("accessible required-file preflight = %d %q", page.status, page.body)
	}
	if strings.Contains(page.body, "sha256/") {
		t.Fatalf("Backstage exposed Attachment storage path: %q", page.body)
	}

	configured := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                  {requireFrontendCSRF(t, page)},
		"action":                      {"configure-readiness"},
		"command_id":                  {"browser-configure-readiness"},
		"expected_readiness_revision": {"0"},
		"require_entry_review":        {"true"},
	})
	if configured.status != http.StatusSeeOther {
		t.Fatalf("configure Competition readiness = %d %q", configured.status, configured.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "unresolved_entry_review") ||
		!strings.Contains(page.body, `role="alert"`) {
		t.Fatalf("accessible review preflight = %d %q", page.status, page.body)
	}

	reviewed := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"review-entry"},
		"command_id":        {"browser-review-entry"},
		"entry_id":          {"1"},
		"expected_revision": {"1"},
	})
	if reviewed.status != http.StatusSeeOther {
		t.Fatalf("review Entry = %d %q", reviewed.status, reviewed.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if strings.Contains(page.body, "Review Outdated") {
		t.Fatalf("reviewed Entry projection = %d %q", page.status, page.body)
	}

	updated := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"update-entry"},
		"command_id":        {"browser-update-entry"},
		"entry_id":          {"1"},
		"expected_revision": {"2"},
		"entry_name":        {"Project Aurora Revised"},
		"public_details":    {"A revised public abstract"},
		"crew_notes":        {"Revised Crew-only note"},
	})
	if updated.status != http.StatusSeeOther {
		t.Fatalf("edit Entry = %d %q", updated.status, updated.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Project Aurora Revised") ||
		!strings.Contains(page.body, "Review Outdated") {
		t.Fatalf("Entry edit did not invalidate review = %d %q", page.status, page.body)
	}
	staleEntry := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"update-entry"},
		"command_id":        {"browser-stale-entry"},
		"entry_id":          {"1"},
		"expected_revision": {"2"},
		"entry_name":        {"Safe stale Entry"},
		"public_details":    {"Safe stale public details"},
		"crew_notes":        {"Safe stale Crew notes"},
	})
	if staleEntry.status != http.StatusConflict ||
		!strings.Contains(staleEntry.body, `value="Safe stale Entry"`) {
		t.Fatalf("stale Entry update = %d %q", staleEntry.status, staleEntry.body)
	}
	assertAccessibleFormErrors(t, staleEntry, nil)

	rejected := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"change-disposition"},
		"command_id":        {"browser-reject-entry"},
		"entry_id":          {"1"},
		"expected_revision": {"3"},
		"disposition":       {"Rejected"},
	})
	if rejected.status != http.StatusSeeOther {
		t.Fatalf("reject Entry = %d %q", rejected.status, rejected.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, `data-tone="danger">Rejected</span>`) {
		t.Fatalf("rejected Entry projection = %d %q", page.status, page.body)
	}

	included := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"change-disposition"},
		"command_id":        {"browser-include-entry"},
		"entry_id":          {"1"},
		"expected_revision": {"4"},
		"disposition":       {"Included"},
	})
	if included.status != http.StatusSeeOther {
		t.Fatalf("include Entry = %d %q", included.status, included.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	second := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, page)},
		"action":         {"create-entry"},
		"command_id":     {"browser-create-second-entry"},
		"entry_name":     {"Project Borealis"},
		"public_details": {"Second public abstract"},
	})
	if second.status != http.StatusSeeOther {
		t.Fatalf("create second Entry = %d %q", second.status, second.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !regexp.MustCompile(`<select[^>]*name="manual_entry_ids"`).MatchString(page.body) ||
		!strings.Contains(page.body, "Project Aurora Revised") ||
		!strings.Contains(page.body, "Project Borealis") {
		t.Fatalf("manual Entry order lacks a named picker: %d %q", page.status, page.body)
	}
	orderRevision := frontendNamedValues(page.body, "expected_order_revision").
		Get("expected_order_revision")
	reordered := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"configure-order"},
		"command_id":              {"browser-reorder-entries"},
		"expected_order_revision": {orderRevision},
		"order_policy":            {"ManualOrder"},
		"manual_entry_ids":        {"2", "1"},
	})
	if reordered.status != http.StatusSeeOther {
		t.Fatalf("reorder Entries = %d %q", reordered.status, reordered.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !regexp.MustCompile(`Canonical order:\s+2,\s+1`).MatchString(page.body) {
		t.Fatalf("manual Entry order projection = %d %q", page.status, page.body)
	}
	// The position pickers default every select to the current Manual order
	// even when a Producer switches away from Manual order, so a browser
	// resubmits nonempty manual_entry_ids alongside the new policy. Neither
	// Submission order nor Deterministic shuffle accept Manual Entry IDs;
	// confirm the switch still succeeds and does not retain them.
	backToSubmissionOrder := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"configure-order"},
		"command_id":              {"browser-restore-submission-order"},
		"expected_order_revision": {frontendNamedValues(page.body, "expected_order_revision").Get("expected_order_revision")},
		"order_policy":            {"SubmissionOrder"},
		"manual_entry_ids":        {"2", "1"},
	})
	if backToSubmissionOrder.status != http.StatusSeeOther {
		t.Fatalf(
			"restore Submission order despite stale Manual selections = %d %q",
			backToSubmissionOrder.status, backToSubmissionOrder.body,
		)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !regexp.MustCompile(`Canonical order:\s+1,\s+2`).MatchString(page.body) {
		t.Fatalf("Submission order projection after policy switch = %d %q", page.status, page.body)
	}
	manualOrderFieldset := regexp.MustCompile(
		`(?s)<fieldset id="configure-order-manual-entry-ids">.*?</fieldset>`,
	).FindString(page.body)
	if manualOrderFieldset == "" || strings.Contains(manualOrderFieldset, "selected") {
		t.Fatalf(
			"Manual order pickers remain preselected outside Manual order: %d %q",
			page.status, manualOrderFieldset,
		)
	}
	restoreManualOrder := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":              {requireFrontendCSRF(t, page)},
		"action":                  {"configure-order"},
		"command_id":              {"browser-restore-manual-order"},
		"expected_order_revision": {frontendNamedValues(page.body, "expected_order_revision").Get("expected_order_revision")},
		"order_policy":            {"ManualOrder"},
		"manual_entry_ids":        {"2", "1"},
	})
	if restoreManualOrder.status != http.StatusSeeOther {
		t.Fatalf("restore Manual order = %d %q", restoreManualOrder.status, restoreManualOrder.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	reviewedBeforeUpload := postFrontendForm(
		t,
		administrator,
		server.address,
		path,
		url.Values{
			"csrf_token":        {requireFrontendCSRF(t, page)},
			"action":            {"review-entry"},
			"command_id":        {"browser-review-before-upload"},
			"entry_id":          {"1"},
			"expected_revision": {frontendEntryRevision(t, page.body, 1)},
		},
	)
	if reviewedBeforeUpload.status != http.StatusSeeOther {
		t.Fatalf(
			"review Entry before Attachment replacement = %d %q",
			reviewedBeforeUpload.status,
			reviewedBeforeUpload.body,
		)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	reviewedEntryOneRevision := frontendEntryRevision(t, page.body, 1)
	entryOneArticle := frontendEntryArticle(t, page.body, 1)
	wantCurrent := `data-tone="success">Included</span><span>revision <code>` +
		reviewedEntryOneRevision + `</code></span>`
	if !strings.Contains(entryOneArticle, wantCurrent) {
		t.Fatalf(
			"review before Attachment replacement lacks current Entry #1 %q: %d %q",
			wantCurrent, page.status, entryOneArticle,
		)
	}

	uploadPath := path + "/upload"
	invalidUpload := requestMultipart(
		t.Context(),
		administrator,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, page),
			"command_id": "browser-invalid-upload",
			"entry_id":   "1",
			"name":       "",
			"crew_only":  "true",
		},
		"safe.txt",
		"text/plain",
		[]byte("must not persist"),
	)
	if invalidUpload.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidUpload.body, `name="crew_only" value="true" checked`) {
		t.Fatalf("invalid browser Attachment upload = %d %q", invalidUpload.status, invalidUpload.body)
	}
	assertAccessibleFormErrors(t, frontendResponse{
		status: invalidUpload.status, header: invalidUpload.header, body: invalidUpload.body,
	}, map[string]string{
		"upload-entry-1-name": "Enter an Attachment name.",
	})

	firstUpload := requestMultipart(
		t.Context(),
		administrator,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, page),
			"command_id": "browser-upload-v1",
			"entry_id":   "1",
			"name":       "slides",
		},
		"slides-v1.txt",
		"text/plain",
		[]byte("first immutable version"),
	)
	if firstUpload.status != http.StatusSeeOther {
		t.Fatalf("first browser Attachment upload = %d %q", firstUpload.status, firstUpload.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(frontendEntryArticle(t, page.body, 1), "Review Outdated") {
		t.Fatalf("Attachment replacement did not invalidate Entry review: %q", page.body)
	}
	foreignVersion := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"version-release-hold"},
		"command_id":        {"browser-foreign-version"},
		"version_id":        {"999"},
		"expected_revision": {"0"},
		"hold":              {"true"},
	})
	if foreignVersion.status != http.StatusNotFound {
		t.Fatalf(
			"foreign Attachment Version target = %d %q",
			foreignVersion.status,
			foreignVersion.body,
		)
	}
	downloaded := getFrontendPage(
		t,
		administrator,
		server.address,
		"/crew/events/1/attachment-versions/1",
	)
	if downloaded.status != http.StatusOK ||
		downloaded.body != "first immutable version" {
		t.Fatalf("verified immutable Attachment read = %d %q", downloaded.status, downloaded.body)
	}
	secondUpload := requestMultipart(
		t.Context(),
		administrator,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, page),
			"command_id": "browser-upload-v2",
			"entry_id":   "1",
			"name":       "slides",
		},
		"slides-v2.txt",
		"text/plain",
		[]byte("second immutable version"),
	)
	if secondUpload.status != http.StatusSeeOther {
		t.Fatalf("replacement browser Attachment upload = %d %q", secondUpload.status, secondUpload.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{
		"slides-v1.txt",
		"slides-v2.txt",
		"Version 1",
		"Version 2",
		"SHA-256",
		"Release Eligibility: Public",
	} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("immutable Attachment history lacks %q: %q", want, page.body)
		}
	}
	if strings.Contains(page.body, "sha256/") {
		t.Fatalf("Attachment history exposed storage path: %q", page.body)
	}

	ready := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"attachment-readiness"},
		"command_id":        {"browser-ready-v2"},
		"entry_id":          {"1"},
		"version_id":        {"2"},
		"expected_revision": {"1"},
		"final":             {"true"},
		"primary":           {"true"},
	})
	if ready.status != http.StatusSeeOther {
		t.Fatalf("set Final Primary Attachment = %d %q", ready.status, ready.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, `data-tone="success">Final</span>`) ||
		!strings.Contains(page.body, `data-tone="success">Primary</span>`) {
		t.Fatalf("Attachment readiness projection = %d %q", page.status, page.body)
	}

	held := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"version-release-hold"},
		"command_id":        {"browser-hold-v2"},
		"version_id":        {"2"},
		"expected_revision": {"0"},
		"hold":              {"true"},
	})
	if held.status != http.StatusSeeOther {
		t.Fatalf("hold Attachment Version release = %d %q", held.status, held.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, `data-tone="warning">Release Held</span>`) {
		t.Fatalf("Attachment release hold projection = %d %q", page.status, page.body)
	}

	policy := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"competition-release-policy"},
		"command_id":        {"browser-release-policy"},
		"expected_revision": {"0"},
		"override":          {"true"},
		"release_policy":    {"OnEnded"},
	})
	if policy.status != http.StatusSeeOther {
		t.Fatalf("configure Competition Attachment release = %d %q", policy.status, policy.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Competition override: On Ended") {
		t.Fatalf("Competition Attachment release projection = %d %q", page.status, page.body)
	}

	expiresAt := time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")
	invalidReopen := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"create-reopen-window"},
		"command_id": {"browser-invalid-reopen-entry"},
		"entry_id":   {"1"},
		"reason":     {""},
		"expires_at": {"not-a-date"},
	})
	if invalidReopen.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid Reopen Window = %d %q", invalidReopen.status, invalidReopen.body)
	}
	assertAccessibleFormErrors(t, invalidReopen, map[string]string{
		"create-reopen-window-1-reason":     "is required",
		"create-reopen-window-1-expires-at": "must be a valid local date and time",
	})
	reopened := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, invalidReopen)},
		"action":     {"create-reopen-window"},
		"command_id": {"browser-reopen-entry"},
		"entry_id":   {"1"},
		"reason":     {"Late corrected slides"},
		"expires_at": {expiresAt},
	})
	if reopened.status != http.StatusSeeOther {
		t.Fatalf("create Reopen Window = %d %q", reopened.status, reopened.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Late corrected slides") ||
		!strings.Contains(page.body, "Active Reopen Window") ||
		!strings.Contains(page.body, `name="action" value="extend-reopen-window"`) ||
		!strings.Contains(page.body, `name="action" value="close-reopen-window"`) ||
		!strings.Contains(page.body, `name="confirm_close" value="true"`) {
		t.Fatalf("bounded Reopen Window projection = %d %q", page.status, page.body)
	}
	extendedExpiry := time.Now().UTC().Add(4 * time.Hour).Format("2006-01-02T15:04")
	extended := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"browser-extend-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"1"},
		"expires_at":        {extendedExpiry},
	})
	if extended.status != http.StatusSeeOther {
		t.Fatalf("extend Reopen Window = %d %q", extended.status, extended.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	staleExpiry := time.Now().UTC().Add(5 * time.Hour).Format("2006-01-02T15:04")
	stale := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"browser-stale-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"1"},
		"expires_at":        {staleExpiry},
	})
	if stale.status != http.StatusConflict ||
		!strings.Contains(stale.body, "Attachment state changed. Reload and try again.") ||
		!strings.Contains(stale.body, `value="`+staleExpiry+`"`) {
		t.Fatalf("stale Reopen Window extension = %d %q", stale.status, stale.body)
	}
	assertAccessibleFormErrors(t, stale, nil)
	page = getFrontendPage(t, administrator, server.address, path)
	invalid := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"browser-invalid-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"2"},
		"expires_at":        {expiresAt},
	})
	if invalid.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalid.body, "Choose an expiry later than the current expiry.") {
		t.Fatalf("invalid Reopen Window extension = %d %q", invalid.status, invalid.body)
	}
	assertAccessibleFormErrors(t, invalid, map[string]string{
		"extend-reopen-window-1-1-expires-at": "Choose an expiry later than the current expiry.",
	})
	if !strings.Contains(frontendEntryArticle(t, invalid.body, 1), "<details open>") {
		t.Fatalf(
			"invalid Reopen Window extension leaves Entry #1's section collapsed: %q",
			invalid.body,
		)
	}
	if strings.Contains(frontendEntryArticle(t, invalid.body, 2), "<details open>") {
		t.Fatalf(
			"invalid Reopen Window extension expands Entry #2's unrelated section: %q",
			invalid.body,
		)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	unbounded := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"browser-unbounded-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"2"},
		"expires_at":        {time.Now().UTC().Add(8 * 24 * time.Hour).Format("2006-01-02T15:04")},
	})
	if unbounded.status != http.StatusUnprocessableEntity ||
		!strings.Contains(unbounded.body, "Choose a future expiry within 7 days.") {
		t.Fatalf("unbounded Reopen Window extension = %d %q", unbounded.status, unbounded.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	otherWindow := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"create-reopen-window"},
		"command_id": {"browser-reopen-other-entry"},
		"entry_id":   {"2"},
		"reason":     {"Other Entry correction"},
		"expires_at": {time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")},
	})
	if otherWindow.status != http.StatusSeeOther {
		t.Fatalf("create other Entry Reopen Window = %d %q", otherWindow.status, otherWindow.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	forged := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"close-reopen-window"},
		"command_id":        {"browser-close-forged-owner"},
		"entry_id":          {"1"},
		"window_id":         {"2"},
		"expected_revision": {"1"},
		"confirm_close":     {"true"},
	})
	if forged.status != http.StatusNotFound {
		t.Fatalf("cross-owner Reopen Window closure = %d %q", forged.status, forged.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if strings.Count(page.body, "Active Reopen Window") != 2 {
		t.Fatalf("cross-owner closure changed Reopen Window = %d %q", page.status, page.body)
	}
	closedOther := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"close-reopen-window"},
		"command_id":        {"browser-close-other-entry"},
		"entry_id":          {"2"},
		"window_id":         {"2"},
		"expected_revision": {"1"},
		"confirm_close":     {"true"},
	})
	if closedOther.status != http.StatusSeeOther {
		t.Fatalf("close other Entry Reopen Window = %d %q", closedOther.status, closedOther.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	unconfirmed := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"close-reopen-window"},
		"command_id":        {"browser-unconfirmed-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"2"},
	})
	if unconfirmed.status != http.StatusUnprocessableEntity {
		t.Fatalf("unconfirmed Reopen Window closure = %d %q", unconfirmed.status, unconfirmed.body)
	}
	assertAccessibleFormErrors(t, unconfirmed, map[string]string{
		"close-reopen-window-1-1-confirm-close": "must be checked",
	})
	page = getFrontendPage(t, administrator, server.address, path)
	closedWindow := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"close-reopen-window"},
		"command_id":        {"browser-close-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"2"},
		"confirm_close":     {"true"},
	})
	if closedWindow.status != http.StatusSeeOther {
		t.Fatalf("close Reopen Window = %d %q", closedWindow.status, closedWindow.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "<details>") ||
		!strings.Contains(page.body, "Reopen Window history") ||
		!strings.Contains(page.body, "Late corrected slides") ||
		strings.Contains(page.body, "Active Reopen Window") {
		t.Fatalf("closed Reopen Window history = %d %q", page.status, page.body)
	}
	historicalUpdate := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"browser-extend-closed-entry-window"},
		"entry_id":          {"1"},
		"window_id":         {"1"},
		"expected_revision": {"3"},
		"expires_at":        {time.Now().UTC().Add(5 * time.Hour).Format("2006-01-02T15:04")},
	})
	if historicalUpdate.status != http.StatusUnprocessableEntity ||
		!strings.Contains(historicalUpdate.body, "Only active Reopen Windows can be changed.") {
		t.Fatalf(
			"closed Reopen Window update = %d %q",
			historicalUpdate.status,
			historicalUpdate.body,
		)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	reopenedAgain := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"create-reopen-window"},
		"command_id": {"browser-reopen-entry-again"},
		"entry_id":   {"1"},
		"reason":     {"Final corrected slides"},
		"expires_at": {time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")},
	})
	if reopenedAgain.status != http.StatusSeeOther {
		t.Fatalf("create replacement Reopen Window = %d %q", reopenedAgain.status, reopenedAgain.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	failure := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"record-technical-failure"},
		"command_id":        {"browser-record-failure"},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, page.body, 1)},
		"crew_reason":       {"Projector lost signal"},
	})
	if failure.status != http.StatusSeeOther {
		t.Fatalf("record browser Technical Failure = %d %q", failure.status, failure.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	heldEntry := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"entry-release-hold"},
		"command_id":        {"browser-hold-entry"},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, page.body, 1)},
		"hold":              {"true"},
		"crew_reason":       {"Awaiting organizer approval"},
	})
	if heldEntry.status != http.StatusSeeOther {
		t.Fatalf("hold Entry release = %d %q", heldEntry.status, heldEntry.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Projector lost signal") ||
		!strings.Contains(page.body, `data-tone="warning">Release Held</span>`) {
		t.Fatalf("independent Entry exception state = %d %q", page.status, page.body)
	}
	setCompetitionSubmissionDeadline(
		t,
		administrator,
		server,
		competitionID,
		time.Now().UTC().Add(-time.Minute),
	)
	page = getFrontendPage(t, administrator, server.address, path)
	closed := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token": {requireFrontendCSRF(t, page)},
		"action":     {"create-entry"},
		"command_id": {"browser-after-deadline"},
		"entry_name": {"Too late"},
	})
	if closed.status != http.StatusGone ||
		!strings.Contains(closed.body, "fixed Submission Deadline") {
		t.Fatalf("fixed browser Submission Deadline = %d %q", closed.status, closed.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	reopenedUpload := requestMultipart(
		t.Context(),
		administrator,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, page),
			"command_id": "browser-upload-reopened",
			"entry_id":   "1",
			"name":       "slides",
		},
		"slides-v3.txt",
		"text/plain",
		[]byte("upload through bounded reopen window"),
	)
	if reopenedUpload.status != http.StatusSeeOther {
		t.Fatalf("browser upload in Reopen Window = %d %q", reopenedUpload.status, reopenedUpload.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "slides-v3.txt") ||
		!strings.Contains(page.body, "Version 3") {
		t.Fatalf("reopened immutable Attachment Version = %d %q", page.status, page.body)
	}
	crewOnlyUpload := requestMultipart(
		t.Context(),
		administrator,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, page),
			"command_id": "browser-upload-crew-only",
			"entry_id":   "1",
			"name":       "organizer notes",
			"crew_only":  "true",
		},
		"organizer.txt",
		"text/plain",
		[]byte("crew-only file"),
	)
	if crewOnlyUpload.status != http.StatusSeeOther {
		t.Fatalf("Crew Only Attachment upload = %d %q", crewOnlyUpload.status, crewOnlyUpload.body)
	}
	page = getFrontendPage(t, administrator, server.address, path)
	if !strings.Contains(page.body, "Release Eligibility: Crew Only") {
		t.Fatalf("immutable Release Eligibility projection = %d %q", page.status, page.body)
	}
	if public := getFrontendPage(
		t, authenticatedClient(t), server.publicAddress, path,
	); public.status != http.StatusNotFound {
		t.Fatalf("public-listener Entries = %d, want 404", public.status)
	}
	server.stop(t)
}

func exercisePresentationSubmissionFlow(
	t *testing.T,
	administrator, alex, blair *http.Client,
	server *runningServer,
	presentationID int64,
) {
	t.Helper()
	submissionsPath := "/my-participation"
	producerPath := "/backstage/events/1/presentations/" +
		strconv.FormatInt(presentationID, 10) + "/submission"
	uploadPath := "/submissions/1/presentations/" +
		strconv.FormatInt(presentationID, 10) + "/upload"

	alexPage := getFrontendPage(t, alex, server.address, submissionsPath)
	if strings.Contains(alexPage.body, "Create Presentation") ||
		strings.Contains(alexPage.body, "Propose Presentation") {
		t.Fatalf("self-service Presentation proposal exposed: %q", alexPage.body)
	}
	producerPage := getFrontendPage(t, administrator, server.address, producerPath)
	if producerPage.status != http.StatusOK ||
		!strings.Contains(producerPage.body, "Crew Managed") {
		t.Fatalf("Crew Managed Presentation = %d %q", producerPage.status, producerPage.body)
	}
	assigned := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, producerPage)},
		"action":            {"assign-submitter"},
		"command_id":        {"assign-presentation-alex"},
		"expected_revision": {frontendNamedValues(producerPage.body, "expected_revision").Get("expected_revision")},
		"account_id":        {"2"},
	})
	if assigned.status != http.StatusSeeOther {
		t.Fatalf("assign Presentation Submitter = %d %q", assigned.status, assigned.body)
	}
	staleAssignment := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, producerPage)},
		"action":            {"assign-submitter"},
		"command_id":        {"stale-presentation-assignment"},
		"expected_revision": {frontendNamedValues(producerPage.body, "expected_revision").Get("expected_revision")},
		"account_id":        {"3"},
	})
	if staleAssignment.status != http.StatusConflict ||
		!strings.Contains(staleAssignment.body, `value="3" selected`) {
		t.Fatalf("stale Presentation assignment = %d %q", staleAssignment.status, staleAssignment.body)
	}
	assertAccessibleFormErrors(t, staleAssignment, nil)

	presentationPath := "/events/beamconf-2099/schedule/sessions/" +
		strconv.FormatInt(presentationID, 10)
	presentationPage := getFrontendPage(
		t,
		alex,
		server.address,
		presentationPath,
	)
	submissionsPath = frontendLinkPath(t, presentationPage, "Manage Presentation")
	if want := "/my-participation#presentation-" +
		strconv.FormatInt(presentationID, 10) + "-manage"; submissionsPath != want {
		t.Fatalf("Presentation maintenance path = %q, want %q", submissionsPath, want)
	}
	uploadParticipationPath := frontendLinkPath(
		t,
		presentationPage,
		"Upload Presentation File",
	)
	if want := "/my-participation#presentation-" +
		strconv.FormatInt(presentationID, 10) +
		"-upload"; uploadParticipationPath != want {
		t.Fatalf("Presentation upload path = %q, want %q", uploadParticipationPath, want)
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	if !strings.Contains(alexPage.body, "<h2>Presentations</h2>") ||
		!strings.Contains(alexPage.body, "Opening Keynote") {
		t.Fatalf("assigned Presentation listing = %d %q", alexPage.status, alexPage.body)
	}
	if got := frontendLinkPath(t, alexPage, "View Presentation"); got != presentationPath {
		t.Fatalf("Presentation context path = %q, want %q", got, presentationPath)
	}
	invalidSpeaker := strings.Repeat("s", 201)
	invalidPresentationDetails := strings.Repeat("d", 10001)
	invalidPresentation := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, alexPage)},
		"action":            {"update-presentation"},
		"command_id":        {"alex-invalid-presentation"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(presentationID, 10)},
		"expected_revision": {frontendPresentationRevision(t, alexPage.body, presentationID)},
		"speaker":           {invalidSpeaker},
		"public_details":    {invalidPresentationDetails},
	})
	if invalidPresentation.status != http.StatusUnprocessableEntity {
		t.Fatalf(
			"invalid assigned Presentation = %d %q",
			invalidPresentation.status,
			invalidPresentation.body,
		)
	}
	prefix := "presentation-" + strconv.FormatInt(presentationID, 10)
	assertAccessibleFormErrors(t, invalidPresentation, map[string]string{
		prefix + "-speaker": "Enter no more than 200 visible characters.",
		prefix + "-details": "Enter no more than 10000 characters.",
	})
	if !strings.Contains(invalidPresentation.body, invalidSpeaker) ||
		!strings.Contains(invalidPresentation.body, invalidPresentationDetails) {
		t.Fatalf("invalid Presentation values = %q", invalidPresentation.body)
	}
	updated := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, invalidPresentation)},
		"action":            {"update-presentation"},
		"command_id":        {"alex-update-presentation"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(presentationID, 10)},
		"expected_revision": {frontendPresentationRevision(t, alexPage.body, presentationID)},
		"speaker":           {"Alex Public Speaker"},
		"public_details":    {"Alex approved public details"},
	})
	if updated.status != http.StatusSeeOther {
		t.Fatalf("update assigned Presentation = %d %q", updated.status, updated.body)
	}
	for version, body := range [][]byte{
		[]byte("first immutable slides"),
		[]byte("second immutable slides"),
	} {
		alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
		uploaded := requestMultipart(
			t.Context(),
			alex,
			server.address,
			uploadPath,
			map[string]string{
				"csrf_token": requireFrontendCSRF(t, alexPage),
				"command_id": "alex-presentation-upload-" + strconv.Itoa(version+1),
				"name":       "slides",
			},
			"slides-v"+strconv.Itoa(version+1)+".pdf",
			"application/pdf",
			body,
		)
		if uploaded.status != http.StatusSeeOther {
			t.Fatalf(
				"Presentation upload Version %d = %d %q",
				version+1,
				uploaded.status,
				uploaded.body,
			)
		}
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	for _, want := range []string{
		"2 immutable Attachment Versions",
		"slides-v1.pdf",
		"Version 1",
		"slides-v2.pdf",
		"Version 2",
	} {
		if !strings.Contains(alexPage.body, want) {
			t.Fatalf("Presentation version history lacks %q: %q", want, alexPage.body)
		}
	}

	producerPage = getFrontendPage(t, administrator, server.address, producerPath)
	replaced := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, producerPage)},
		"action":            {"assign-submitter"},
		"command_id":        {"replace-presentation-with-blair"},
		"expected_revision": {frontendNamedValues(producerPage.body, "expected_revision").Get("expected_revision")},
		"account_id":        {"3"},
	})
	if replaced.status != http.StatusSeeOther {
		t.Fatalf("replace Presentation Submitter = %d %q", replaced.status, replaced.body)
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	if strings.Contains(alexPage.body, "Opening Keynote") ||
		strings.Contains(alexPage.body, "slides-v2.pdf") {
		t.Fatalf("former Presentation Submitter retained access: %q", alexPage.body)
	}
	presentationPage = getFrontendPage(t, alex, server.address, presentationPath)
	if !strings.Contains(presentationPage.body, "Presentation maintenance is unavailable.") ||
		strings.Contains(presentationPage.body, "Blair Submitter") {
		t.Fatalf(
			"neutral unavailable Presentation state = %d %q",
			presentationPage.status,
			presentationPage.body,
		)
	}
	formerUpload := requestMultipart(
		t.Context(),
		alex,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, alexPage),
			"command_id": "former-submitter-upload",
			"name":       "must-not-parse",
		},
		"private.pdf",
		"application/pdf",
		[]byte("must remain private"),
	)
	if formerUpload.status != http.StatusNotFound {
		t.Fatalf("former Submitter upload = %d %q", formerUpload.status, formerUpload.body)
	}
	blairPage := getFrontendPage(t, blair, server.address, submissionsPath)
	for _, want := range []string{
		"Opening Keynote",
		"Alex Public Speaker",
		"Alex approved public details",
		"slides-v1.pdf",
		"slides-v2.pdf",
	} {
		if !strings.Contains(blairPage.body, want) {
			t.Fatalf("replacement Presentation projection lacks %q: %q", want, blairPage.body)
		}
	}

	rundownClient := connectClient(rundownv1connect.NewRundownServiceClient, administrator, server.address)
	current, err := rundownClient.GetCrewRundown(
		t.Context(),
		connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before Presentation deadline: %v", err)
	}
	setPresentationUploadDeadline(
		t,
		rundownClient,
		current.Msg.GetDraftRevision(),
		presentationID,
		time.Now().UTC().Add(-time.Minute),
	)
	blairPage = getFrontendPage(t, blair, server.address, submissionsPath)
	closed := postFrontendForm(t, blair, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, blairPage)},
		"action":            {"update-presentation"},
		"command_id":        {"blair-update-closed-presentation"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(presentationID, 10)},
		"expected_revision": {frontendPresentationRevision(t, blairPage.body, presentationID)},
		"speaker":           {"Too Late"},
	})
	if closed.status != http.StatusGone {
		t.Fatalf("Presentation fixed Upload Deadline = %d %q", closed.status, closed.body)
	}
	closedUpload := requestMultipart(
		t.Context(),
		blair,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, blairPage),
			"command_id": "blair-presentation-upload-closed",
			"name":       "closed",
		},
		"closed.pdf",
		"application/pdf",
		[]byte("must not persist"),
	)
	if closedUpload.status != http.StatusGone {
		t.Fatalf("closed Presentation upload = %d %q", closedUpload.status, closedUpload.body)
	}

	producerPage = getFrontendPage(t, administrator, server.address, producerPath)
	invalidReopen := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, producerPage)},
		"action":     {"create-reopen-window"},
		"command_id": {"invalid-reopen-presentation-submission"},
		"reason":     {""},
		"expires_at": {"not-a-date"},
	})
	if invalidReopen.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid Presentation Reopen Window = %d %q", invalidReopen.status, invalidReopen.body)
	}
	assertAccessibleFormErrors(t, invalidReopen, map[string]string{
		"create-reopen-window-reason":     "is required",
		"create-reopen-window-expires-at": "must be a valid local date and time",
	})
	reopened := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, invalidReopen)},
		"action":     {"create-reopen-window"},
		"command_id": {"reopen-presentation-submission"},
		"reason":     {"Submitter correction"},
		"expires_at": {time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")},
	})
	if reopened.status != http.StatusSeeOther {
		t.Fatalf("reopen Presentation submission = %d %q", reopened.status, reopened.body)
	}
	producerPage = getFrontendPage(t, administrator, server.address, producerPath)
	if !strings.Contains(producerPage.body, "Submitter correction") ||
		!strings.Contains(producerPage.body, "Active Reopen Window") ||
		!strings.Contains(producerPage.body, `name="action" value="extend-reopen-window"`) ||
		!strings.Contains(producerPage.body, `name="action" value="close-reopen-window"`) {
		t.Fatalf("Presentation Reopen Window projection = %d %q", producerPage.status, producerPage.body)
	}
	extended := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, producerPage)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"extend-presentation-submission"},
		"window_id":         {"1"},
		"expected_revision": {"1"},
		"expires_at":        {time.Now().UTC().Add(4 * time.Hour).Format("2006-01-02T15:04")},
	})
	if extended.status != http.StatusSeeOther {
		t.Fatalf("extend Presentation Reopen Window = %d %q", extended.status, extended.body)
	}
	producerPage = getFrontendPage(t, administrator, server.address, producerPath)
	invalid := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, producerPage)},
		"action":            {"extend-reopen-window"},
		"command_id":        {"invalid-presentation-extension"},
		"window_id":         {"1"},
		"expected_revision": {"2"},
		"expires_at":        {time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")},
	})
	if invalid.status != http.StatusUnprocessableEntity ||
		!strings.Contains(invalid.body, "Choose an expiry later than the current expiry.") {
		t.Fatalf("invalid Presentation Reopen Window extension = %d %q", invalid.status, invalid.body)
	}
	assertAccessibleFormErrors(t, invalid, map[string]string{
		"extend-reopen-window-" + strconv.FormatInt(presentationID, 10) + "-1-expires-at": "Choose an expiry later than the current expiry.",
	})
	producerPage = getFrontendPage(t, administrator, server.address, producerPath)
	closedWindow := postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, producerPage)},
		"action":            {"close-reopen-window"},
		"command_id":        {"close-presentation-submission"},
		"window_id":         {"1"},
		"expected_revision": {"2"},
		"confirm_close":     {"true"},
	})
	if closedWindow.status != http.StatusSeeOther {
		t.Fatalf("close Presentation Reopen Window = %d %q", closedWindow.status, closedWindow.body)
	}
	blairPage = getFrontendPage(t, blair, server.address, submissionsPath)
	closedAgain := requestMultipart(
		t.Context(),
		blair,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, blairPage),
			"command_id": "blair-presentation-upload-closed-again",
			"name":       "closed-again",
		},
		"closed-again.pdf",
		"application/pdf",
		[]byte("must not persist"),
	)
	if closedAgain.status != http.StatusGone {
		t.Fatalf("early-closed Presentation upload = %d %q", closedAgain.status, closedAgain.body)
	}
	producerPage = getFrontendPage(t, administrator, server.address, producerPath)
	if !strings.Contains(producerPage.body, "Reopen Window history") ||
		!strings.Contains(producerPage.body, "Submitter correction") {
		t.Fatalf("Presentation Reopen Window history = %d %q", producerPage.status, producerPage.body)
	}
	reopened = postFrontendForm(t, administrator, server.address, producerPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, producerPage)},
		"action":     {"create-reopen-window"},
		"command_id": {"reopen-presentation-submission-again"},
		"reason":     {"Final Submitter correction"},
		"expires_at": {time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")},
	})
	if reopened.status != http.StatusSeeOther {
		t.Fatalf("reopen Presentation submission again = %d %q", reopened.status, reopened.body)
	}
	blairPage = getFrontendPage(t, blair, server.address, submissionsPath)
	reopenedUpdate := postFrontendForm(t, blair, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, blairPage)},
		"action":            {"update-presentation"},
		"command_id":        {"blair-update-reopened-presentation"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(presentationID, 10)},
		"expected_revision": {frontendPresentationRevision(t, blairPage.body, presentationID)},
		"speaker":           {"Blair Final Speaker"},
		"public_details":    {"Blair final approved details"},
	})
	if reopenedUpdate.status != http.StatusSeeOther {
		t.Fatalf("update Presentation in Reopen Window = %d %q", reopenedUpdate.status, reopenedUpdate.body)
	}
	blairPage = getFrontendPage(t, blair, server.address, submissionsPath)
	reopenedUpload := requestMultipart(
		t.Context(),
		blair,
		server.address,
		uploadPath,
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, blairPage),
			"command_id": "blair-presentation-upload-reopened",
			"name":       "slides",
		},
		"slides-v3.pdf",
		"application/pdf",
		[]byte("third immutable slides"),
	)
	if reopenedUpload.status != http.StatusSeeOther {
		t.Fatalf("reopened Presentation upload = %d %q", reopenedUpload.status, reopenedUpload.body)
	}
	blairPage = getFrontendPage(t, blair, server.address, submissionsPath)
	if !strings.Contains(blairPage.body, "slides-v3.pdf") ||
		!strings.Contains(blairPage.body, "Version 3") {
		t.Fatalf("reopened Presentation Version = %d %q", blairPage.status, blairPage.body)
	}

	audit := get(t, administrator, server.address, "/admin/audit")
	auditBody, err := io.ReadAll(audit.Body)
	_ = audit.Body.Close()
	if err != nil ||
		bytes.Count(auditBody, []byte(`"action":"AssignPresentationSubmitter"`)) != 3 ||
		!bytes.Contains(auditBody, []byte(`"reason":"stale_presentation_submission"`)) ||
		!bytes.Contains(auditBody, []byte(`"note":"Submitter Account #3"`)) {
		t.Fatalf("Presentation assignment Audit evidence = %s (%v)", auditBody, err)
	}
	publicSchedule := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		"/events/beamconf-2099/schedule",
	)
	if publicSchedule.status != http.StatusOK ||
		!strings.Contains(publicSchedule.body, "Blair Final Speaker") ||
		!strings.Contains(publicSchedule.body, "Blair final approved details") {
		t.Fatalf("approved public Presentation projection = %d %q", publicSchedule.status, publicSchedule.body)
	}
}

func TestAccountSubmissionsHonorPolicyOwnershipAndReopenWindows(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	presentationID := prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	entriesPath := "/backstage/events/1/competitions/" +
		strconv.FormatInt(competitionID, 10) + "/entries"

	settingsPath := "/backstage/events/1/settings"
	settings := getFrontendPage(t, administrator, server.address, settingsPath)
	eventRevision := frontendNamedValues(settings.body, "expected_event_revision").
		Get("expected_event_revision")
	published := postFrontendForm(t, administrator, server.address, settingsPath, url.Values{
		"csrf_token":                        {requireFrontendCSRF(t, settings)},
		"command_id":                        {"publish-submission-event"},
		"expected_event_revision":           {eventRevision},
		"event_name":                        {"BeamConf 2099"},
		"public":                            {"true"},
		"planned_start_date":                {"2099-08-21"},
		"planned_end_date":                  {"2099-08-23"},
		"timezone":                          {"Europe/Berlin"},
		"event_locale":                      {"en-GB"},
		"content_language":                  {"en-GB"},
		"event_day_boundary":                {"06:00"},
		"entry_default_disposition":         {"Pending"},
		"submission_eligibility":            {"AllAccounts"},
		"voting_method":                     {"Range1To5"},
		"self_vote_policy":                  {"Allowed"},
		"target_adjustment_presets_seconds": {"-300,300,600"},
	})
	if published.status != http.StatusSeeOther {
		t.Fatalf("publish submission Event = %d %q", published.status, published.body)
	}

	const password = "submission correct horse battery staple"
	for id, name := range []string{"Alex Submitter", "Blair Submitter"} {
		assertJSONRequest(
			t,
			administrator,
			server.address,
			"/admin/accounts",
			map[string]string{
				"name": name, "password": password,
				"command_id": "create-submitter-" + strconv.Itoa(id+2),
			},
			http.StatusCreated,
			"{\"id\":"+strconv.Itoa(id+2)+",\"name\":\""+name+"\",\"administrator\":false}\n",
		)
	}
	signIn := func(name string) *http.Client {
		t.Helper()
		client := authenticatedClient(t)
		client.CheckRedirect = administrator.CheckRedirect
		assertJSONRequest(
			t,
			client,
			server.address,
			"/auth/sign-in",
			map[string]string{"name": name, "password": password},
			http.StatusNoContent,
			"",
		)
		return client
	}
	alex := signIn("Alex Submitter")
	blair := signIn("Blair Submitter")
	exercisePresentationSubmissionFlow(
		t,
		administrator,
		alex,
		blair,
		server,
		presentationID,
	)

	competitionPath := "/events/beamconf-2099/competitions/" +
		strconv.FormatInt(competitionID, 10)
	competitionPage := getFrontendPage(t, alex, server.address, competitionPath)
	submissionsPath := frontendLinkPath(t, competitionPage, "Submit")
	if want := "/my-participation#competition-" +
		strconv.FormatInt(competitionID, 10); submissionsPath != want {
		t.Fatalf("Competition submission path = %q, want %q", submissionsPath, want)
	}
	alexPage := getFrontendPage(t, alex, server.address, submissionsPath)
	if alexPage.status != http.StatusOK ||
		!strings.Contains(alexPage.body, "<h1>My Participation</h1>") ||
		!strings.Contains(alexPage.body, `href="/my-participation">My Participation</a>`) ||
		strings.Contains(alexPage.body, `>Submissions</a>`) ||
		strings.Contains(alexPage.body, `>Voting</a>`) ||
		!strings.Contains(alexPage.body, `href="/voting">Redeem a Voting Key</a>`) ||
		!strings.Contains(alexPage.body, "<h2>Entries</h2>") ||
		!strings.Contains(alexPage.body, "Demo Competition") {
		t.Fatalf("Account submission listing = %d %q", alexPage.status, alexPage.body)
	}
	if got := frontendLinkPath(t, alexPage, "View Competition"); got != competitionPath {
		t.Fatalf("Competition context path = %q, want %q", got, competitionPath)
	}
	invalidDetails := strings.Repeat("x", 10001)
	invalid := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, alexPage)},
		"action":         {"create"},
		"command_id":     {"account-create-invalid"},
		"event_id":       {"1"},
		"session_id":     {strconv.FormatInt(competitionID, 10)},
		"entry_name":     {""},
		"public_details": {invalidDetails},
	})
	if invalid.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid Account submission = %d %q", invalid.status, invalid.body)
	}
	assertAccessibleFormErrors(t, invalid, map[string]string{
		"entry-create-" + strconv.FormatInt(competitionID, 10) + "-name":    "Enter an Entry name.",
		"entry-create-" + strconv.FormatInt(competitionID, 10) + "-details": "Enter no more than 10000 characters.",
	})
	if !strings.Contains(invalid.body, invalidDetails) {
		t.Fatalf("invalid Account submission values = %q", invalid.body)
	}
	created := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":     {requireFrontendCSRF(t, alexPage)},
		"action":         {"create"},
		"command_id":     {"account-create-first"},
		"event_id":       {"1"},
		"session_id":     {strconv.FormatInt(competitionID, 10)},
		"entry_name":     {"Alex Public Credit"},
		"public_details": {"First public abstract"},
	})
	if created.status != http.StatusSeeOther {
		t.Fatalf("All Accounts submission = %d %q", created.status, created.body)
	}
	competitionPage = getFrontendPage(t, alex, server.address, competitionPath)
	if got := frontendLinkPath(
		t,
		competitionPage,
		"Manage My Entry",
	); got != submissionsPath {
		t.Fatalf("managed Entry path = %q, want %q", got, submissionsPath)
	}

	entriesPage := getFrontendPage(t, administrator, server.address, entriesPath)
	policyRevision := frontendNamedValues(
		entriesPage.body,
		"expected_submission_eligibility_revision",
	).Get("expected_submission_eligibility_revision")
	tightened := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, entriesPage)},
		"action":     {"configure-submission-eligibility"},
		"command_id": {"require-voting-eligibility"},
		"expected_submission_eligibility_revision": {policyRevision},
		"override":               {"true"},
		"submission_eligibility": {"VotingEligibleAccounts"},
	})
	if tightened.status != http.StatusSeeOther {
		t.Fatalf("tighten Submission Eligibility = %d %q", tightened.status, tightened.body)
	}
	unavailable := getFrontendPage(t, blair, server.address, competitionPath)
	if !strings.Contains(unavailable.body, "Entry submission is unavailable.") ||
		!strings.Contains(unavailable.body, "Voting has not opened.") ||
		strings.Contains(unavailable.body, "VotingEligibleAccounts") {
		t.Fatalf("neutral unavailable submission state = %d %q", unavailable.status, unavailable.body)
	}

	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	if strings.Contains(alexPage.body, "VotingEligibleAccounts") {
		t.Fatalf("My Participation exposed Submission policy: %q", alexPage.body)
	}
	updated := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, alexPage)},
		"action":            {"update"},
		"command_id":        {"account-update-after-tightening"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(competitionID, 10)},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, alexPage.body, 1)},
		"entry_name":        {"Alex Revised Credit"},
		"public_details":    {"Revised public abstract"},
	})
	if updated.status != http.StatusSeeOther {
		t.Fatalf("existing submission after tightening = %d %q", updated.status, updated.body)
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	ineligibleForm := url.Values{
		"csrf_token": {requireFrontendCSRF(t, alexPage)},
		"action":     {"create"},
		"command_id": {"account-create-ineligible"},
		"event_id":   {"1"}, "session_id": {strconv.FormatInt(competitionID, 10)},
		"entry_name": {"Blocked Entry"},
	}
	ineligible := postFrontendForm(t, alex, server.address, submissionsPath, ineligibleForm)
	if ineligible.status != http.StatusForbidden {
		t.Fatalf("Voting Eligible submission = %d %q", ineligible.status, ineligible.body)
	}
	replayedIneligible := postFrontendForm(
		t,
		alex,
		server.address,
		submissionsPath,
		ineligibleForm,
	)
	if replayedIneligible.status != http.StatusForbidden {
		t.Fatalf(
			"replayed Voting Eligible submission = %d %q",
			replayedIneligible.status,
			replayedIneligible.body,
		)
	}
	audit := get(t, administrator, server.address, "/admin/audit")
	auditBody, err := io.ReadAll(audit.Body)
	_ = audit.Body.Close()
	if err != nil ||
		bytes.Count(auditBody, []byte(`"action":"CreateSubmittedCompetitionEntry"`)) != 2 ||
		!bytes.Contains(auditBody, []byte(`"reason":"submission_ineligible"`)) {
		t.Fatalf("Account submission rejection Audit evidence = %s (%v)", auditBody, err)
	}

	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	policyRevision = frontendNamedValues(
		entriesPage.body,
		"expected_submission_eligibility_revision",
	).Get("expected_submission_eligibility_revision")
	opened := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, entriesPage)},
		"action":     {"configure-submission-eligibility"},
		"command_id": {"allow-all-accounts"},
		"expected_submission_eligibility_revision": {policyRevision},
		"override":               {"true"},
		"submission_eligibility": {"AllAccounts"},
	})
	if opened.status != http.StatusSeeOther {
		t.Fatalf("open Submission Eligibility = %d %q", opened.status, opened.body)
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	second := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, alexPage)},
		"action":     {"create"},
		"command_id": {"account-create-second"},
		"event_id":   {"1"}, "session_id": {strconv.FormatInt(competitionID, 10)},
		"entry_name": {"Alex Second Entry"},
	})
	if second.status != http.StatusSeeOther {
		t.Fatalf("Competition All Accounts override = %d %q", second.status, second.body)
	}

	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	crewManaged := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, entriesPage)},
		"action":     {"create-entry"},
		"command_id": {"create-crew-managed-submission"},
		"entry_name": {"Crew Assigned Credit"},
	})
	if crewManaged.status != http.StatusSeeOther {
		t.Fatalf("create Crew Managed Entry = %d %q", crewManaged.status, crewManaged.body)
	}
	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	assigned := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, entriesPage)},
		"action":            {"assign-submitter"},
		"command_id":        {"assign-crew-managed-submission"},
		"entry_id":          {"3"},
		"expected_revision": {frontendEntryRevision(t, entriesPage.body, 3)},
		"account_id":        {"3"},
	})
	if assigned.status != http.StatusSeeOther {
		t.Fatalf("assign Crew Managed Entry = %d %q", assigned.status, assigned.body)
	}

	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	reviewed := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, entriesPage)},
		"action":            {"review-entry"},
		"command_id":        {"review-account-submission"},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, entriesPage.body, 1)},
	})
	if reviewed.status != http.StatusSeeOther {
		t.Fatalf("review Account submission = %d %q", reviewed.status, reviewed.body)
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	invalidated := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, alexPage)},
		"action":            {"update"},
		"command_id":        {"account-invalidate-review"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(competitionID, 10)},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, alexPage.body, 1)},
		"entry_name":        {"Alex Final Credit"},
	})
	if invalidated.status != http.StatusSeeOther {
		t.Fatalf("Account review invalidation = %d %q", invalidated.status, invalidated.body)
	}
	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	if !strings.Contains(frontendEntryArticle(t, entriesPage.body, 1), "Review Outdated") {
		t.Fatalf("Account update retained stale review: %q", entriesPage.body)
	}

	setCompetitionSubmissionDeadline(
		t,
		administrator,
		server,
		competitionID,
		time.Now().UTC().Add(-time.Minute),
	)
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	closed := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, alexPage)},
		"action":            {"update"},
		"command_id":        {"account-update-after-deadline"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(competitionID, 10)},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, alexPage.body, 1)},
		"entry_name":        {"Too Late"},
	})
	if closed.status != http.StatusGone {
		t.Fatalf("Account fixed Submission Deadline = %d %q", closed.status, closed.body)
	}
	for attempt := range 2 {
		closedUpload := requestMultipart(
			t.Context(),
			alex,
			server.address,
			"/submissions/1/entries/1/upload",
			map[string]string{
				"csrf_token": requireFrontendCSRF(t, alexPage),
				"command_id": "account-upload-closed",
				"name":       "closed",
			},
			"closed.zip",
			"application/zip",
			[]byte("must not persist"),
		)
		if closedUpload.status != http.StatusGone {
			t.Fatalf(
				"closed Account upload attempt %d = %d %q",
				attempt+1,
				closedUpload.status,
				closedUpload.body,
			)
		}
	}
	audit = get(t, administrator, server.address, "/admin/audit")
	auditBody, err = io.ReadAll(audit.Body)
	_ = audit.Body.Close()
	if err != nil ||
		bytes.Count(
			auditBody,
			[]byte(
				`"action":"UploadAttachment","target_type":"Entry",`+
					`"target_id":"1","outcome":"Rejected","reason":"upload_closed"`,
			),
		) != 1 {
		t.Fatalf("Account upload rejection Audit evidence = %s (%v)", auditBody, err)
	}

	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	reopened := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token": {requireFrontendCSRF(t, entriesPage)},
		"action":     {"create-reopen-window"},
		"command_id": {"reopen-account-submission"},
		"entry_id":   {"1"},
		"reason":     {"Submitter correction"},
		"expires_at": {time.Now().UTC().Add(3 * time.Hour).Format("2006-01-02T15:04")},
	})
	if reopened.status != http.StatusSeeOther {
		t.Fatalf("reopen Account submission = %d %q", reopened.status, reopened.body)
	}
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	reopenedUpdate := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, alexPage)},
		"action":            {"update"},
		"command_id":        {"account-update-reopened"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(competitionID, 10)},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, alexPage.body, 1)},
		"entry_name":        {"Alex Reopened Credit"},
	})
	if reopenedUpdate.status != http.StatusSeeOther {
		t.Fatalf("update in Reopen Window = %d %q", reopenedUpdate.status, reopenedUpdate.body)
	}
	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	reviewed = postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, entriesPage)},
		"action":            {"review-entry"},
		"command_id":        {"review-before-account-upload"},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, entriesPage.body, 1)},
	})
	if reviewed.status != http.StatusSeeOther {
		t.Fatalf("review before Account upload = %d %q", reviewed.status, reviewed.body)
	}

	content := []byte("immutable Account submission")
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	invalidFilename := strings.Repeat("f", 256)
	invalidUpload := requestMultipart(
		t.Context(),
		alex,
		server.address,
		"/submissions/1/entries/1/upload",
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, alexPage),
			"command_id": "account-upload-invalid",
			"name":       "",
		},
		invalidFilename,
		"application/zip",
		content,
	)
	if invalidUpload.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid Account upload = %d %q", invalidUpload.status, invalidUpload.body)
	}
	assertAccessibleFormErrors(t, frontendResponse{
		status: invalidUpload.status,
		header: invalidUpload.header,
		body:   invalidUpload.body,
	}, map[string]string{
		"entry-1-upload-name": "Enter an Attachment name.",
		"entry-1-upload-file": "Choose a file with a valid name.",
	})
	if strings.Contains(invalidUpload.body, invalidFilename) ||
		strings.Contains(invalidUpload.body, string(content)) {
		t.Fatalf("invalid Account upload retained file secret: %q", invalidUpload.body)
	}
	uploaded := requestMultipart(
		t.Context(),
		alex,
		server.address,
		"/submissions/1/entries/1/upload",
		map[string]string{
			"csrf_token": requireFrontendCSRF(t, alexPage),
			"command_id": "account-upload-reopened",
			"name":       "archive",
		},
		"entry.zip",
		"application/zip",
		content,
	)
	if uploaded.status != http.StatusSeeOther {
		t.Fatalf("Account upload in Reopen Window = %d %q", uploaded.status, uploaded.body)
	}
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	if !strings.Contains(alexPage.body, "entry.zip") ||
		!strings.Contains(alexPage.body, hash) {
		t.Fatalf("Account upload integrity metadata missing: %q", alexPage.body)
	}
	entriesPage = getFrontendPage(t, administrator, server.address, entriesPath)
	if !strings.Contains(frontendEntryArticle(t, entriesPage.body, 1), "Review Outdated") {
		t.Fatalf("Account upload retained stale review: %q", entriesPage.body)
	}
	closedWindow := postFrontendForm(t, administrator, server.address, entriesPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, entriesPage)},
		"action":            {"close-reopen-window"},
		"command_id":        {"close-account-submission"},
		"entry_id":          {"1"},
		"window_id":         {"3"},
		"expected_revision": {"1"},
		"confirm_close":     {"true"},
	})
	if closedWindow.status != http.StatusSeeOther {
		t.Fatalf("close Account Reopen Window = %d %q", closedWindow.status, closedWindow.body)
	}
	server.stop(t)
	server = startBeamersWithPublicListener(t, server.bin, server.dataDir)
	alexPage = getFrontendPage(t, alex, server.address, submissionsPath)
	closedAfterRestart := postFrontendForm(t, alex, server.address, submissionsPath, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, alexPage)},
		"action":            {"update"},
		"command_id":        {"account-update-after-reopen-close"},
		"event_id":          {"1"},
		"session_id":        {strconv.FormatInt(competitionID, 10)},
		"entry_id":          {"1"},
		"expected_revision": {frontendEntryRevision(t, alexPage.body, 1)},
		"entry_name":        {"Too Late Again"},
	})
	if closedAfterRestart.status != http.StatusGone {
		t.Fatalf(
			"closed Account Reopen Window after restart = %d %q",
			closedAfterRestart.status,
			closedAfterRestart.body,
		)
	}
	blairPage := getFrontendPage(t, blair, server.address, submissionsPath)
	if !strings.Contains(blairPage.body, "Crew Assigned Credit") ||
		strings.Contains(blairPage.body, "Alex Reopened Credit") ||
		strings.Contains(blairPage.body, "entry.zip") ||
		strings.Contains(blairPage.body, hash) {
		t.Fatalf("Account submission privacy/assignment = %d %q", blairPage.status, blairPage.body)
	}
	server.stop(t)
}

func TestBrowserDefersAndResolvesCompetitionEntries(t *testing.T) {
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	administrator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	prepareActiveSchedule(t, administrator, server)
	enrollAndAssignGroupedDisplay(
		t, administrator, server, "Competition Display", "competition-output",
		[]string{"program"},
	)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	path := "/backstage/events/1/competitions/" +
		strconv.FormatInt(competitionID, 10) + "/entries"

	names := []string{"Aurora", "Beacon", "Comet"}
	for index, name := range names {
		page := getFrontendPage(t, administrator, server.address, path)
		created := postFrontendForm(t, administrator, server.address, path, url.Values{
			"csrf_token": {requireFrontendCSRF(t, page)},
			"action":     {"create-entry"},
			"command_id": {"browser-live-entry-" + strconv.Itoa(index)},
			"entry_name": {name},
			"crew_notes": {"private " + name},
		})
		if created.status != http.StatusSeeOther {
			t.Fatalf("create live Entry %q = %d %q", name, created.status, created.body)
		}
	}
	page := getFrontendPage(t, administrator, server.address, path)
	if configured := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                  {requireFrontendCSRF(t, page)},
		"action":                      {"configure-readiness"},
		"command_id":                  {"browser-live-readiness"},
		"expected_readiness_revision": {"0"},
	}); configured.status != http.StatusSeeOther {
		t.Fatalf("disable live file delivery = %d %q", configured.status, configured.body)
	}

	operationsPath := "/backstage/events/1/operations"
	operationsPage := getFrontendPage(t, administrator, server.address, operationsPath)
	started := postFrontendForm(t, administrator, server.address, operationsPath, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, operationsPage)},
		"action":                       {"start-session"},
		"command_id":                   {"start-browser-competition"},
		"session_id":                   {strconv.FormatInt(competitionID, 10)},
		"expected_live_state_revision": {"0"},
	})
	if started.status != http.StatusSeeOther {
		t.Fatalf("start browser Competition = %d %q", started.status, started.body)
	}
	operator := provisionOperatorWithScopes(
		t, administrator, server, []int{1}, []string{"program"},
	)
	operator.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	backstage := getFrontendPage(
		t,
		operator,
		server.address,
		"/backstage/events/1/sessions",
	)
	if backstage.status != http.StatusOK ||
		!strings.Contains(backstage.body, `href="`+path+`"`) ||
		!strings.Contains(backstage.body, "Competition Entries and Attachments") {
		t.Fatalf("Operator Competition Entry navigation = %d %q", backstage.status, backstage.body)
	}
	page = getFrontendPage(t, operator, server.address, path)
	if page.status != http.StatusOK ||
		!strings.Contains(page.body, `name="action" value="record-technical-failure"`) ||
		strings.Contains(page.body, `name="action" value="create-entry"`) ||
		strings.Contains(page.body, `name="action" value="resolve-entry"`) {
		t.Fatalf("Operator Competition Entry controls = %d %q", page.status, page.body)
	}
	claimed := postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, page)},
		"action":                    {"claim-control"},
		"command_id":                {"browser-claim-control"},
		"expected_control_revision": {"0"},
	})
	if claimed.status != http.StatusSeeOther {
		t.Fatalf("claim browser Program Control = %d %q", claimed.status, claimed.body)
	}

	programClient := connectClient(programv1connect.NewProgramControlServiceClient, operator, server.address)
	competitionClient := connectClient(competitionv1connect.NewCompetitionServiceClient, administrator, server.address)
	current, err := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("read claimed Program Channel: %v", err)
	}
	channel := current.Msg.GetChannel()
	for _, commandID := range []string{"take-browser-upcoming", "take-browser-starting"} {
		taken, takeErr := programClient.Take(t.Context(), connect.NewRequest(
			&programv1.TakeRequest{
				EventId: 1, SessionId: competitionID, CommandId: commandID,
				ExpectedLiveStateRevision:    channel.GetLiveStateRevision(),
				ExpectedControlStateRevision: channel.GetControlStateRevision(),
				Preview:                      channel.GetPreview(),
			},
		))
		if takeErr != nil {
			t.Fatalf("advance browser Competition: %v", takeErr)
		}
		channel = taken.Msg.GetChannel()
	}

	firstDeferredID := channel.GetNext().GetEntryId()
	page = getFrontendPage(t, operator, server.address, path)
	deferred := postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, page)},
		"action":                    {"defer-entry"},
		"command_id":                {"browser-defer-first"},
		"entry_id":                  {strconv.FormatInt(firstDeferredID, 10)},
		"expected_revision":         {frontendEntryRevision(t, page.body, int(firstDeferredID))},
		"expected_program_revision": {strconv.FormatInt(channel.GetLiveStateRevision(), 10)},
		"expected_control_revision": {strconv.FormatInt(channel.GetControlStateRevision(), 10)},
	})
	if deferred.status != http.StatusSeeOther {
		t.Fatalf("defer first browser Entry = %d %q", deferred.status, deferred.body)
	}
	current, err = programClient.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("read deferred Program Channel: %v", err)
	}
	channel = current.Msg.GetChannel()
	presentedID := channel.GetNext().GetEntryId()
	order, err := competitionClient.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("preview browser Entry Order: %v", err)
	}
	taken, err := programClient.Take(t.Context(), connect.NewRequest(
		&programv1.TakeRequest{
			EventId: 1, SessionId: competitionID, CommandId: "take-browser-presented",
			ExpectedLiveStateRevision:    channel.GetLiveStateRevision(),
			ExpectedControlStateRevision: channel.GetControlStateRevision(),
			Preview:                      channel.GetPreview(),
			ExpectedEntryOrderRevision:   order.Msg.GetEntryOrder().GetRevision(),
			EntryOrderFingerprint:        order.Msg.GetFingerprint(),
		},
	))
	if err != nil {
		t.Fatalf("present browser Entry: %v", err)
	}
	channel = taken.Msg.GetChannel()
	secondDeferredID := channel.GetNext().GetEntryId()
	page = getFrontendPage(t, operator, server.address, path)
	failed := postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":        {requireFrontendCSRF(t, page)},
		"action":            {"record-technical-failure"},
		"command_id":        {"browser-live-failure"},
		"entry_id":          {strconv.FormatInt(secondDeferredID, 10)},
		"expected_revision": {frontendEntryRevision(t, page.body, int(secondDeferredID))},
		"crew_reason":       {"Encoder unavailable"},
	})
	if failed.status != http.StatusSeeOther {
		t.Fatalf("record live Technical Failure = %d %q", failed.status, failed.body)
	}
	page = getFrontendPage(t, operator, server.address, path)
	deferred = postFrontendForm(t, operator, server.address, path, url.Values{
		"csrf_token":                {requireFrontendCSRF(t, page)},
		"action":                    {"defer-entry"},
		"command_id":                {"browser-defer-second"},
		"entry_id":                  {strconv.FormatInt(secondDeferredID, 10)},
		"expected_revision":         {frontendEntryRevision(t, page.body, int(secondDeferredID))},
		"expected_program_revision": {strconv.FormatInt(channel.GetLiveStateRevision(), 10)},
		"expected_control_revision": {strconv.FormatInt(channel.GetControlStateRevision(), 10)},
	})
	if deferred.status != http.StatusSeeOther {
		t.Fatalf("defer second browser Entry = %d %q", deferred.status, deferred.body)
	}

	operationsPage = getFrontendPage(t, operator, server.address, operationsPath)
	endPreview := postFrontendForm(t, operator, server.address, operationsPath, url.Values{
		"csrf_token":                   {requireFrontendCSRF(t, operationsPage)},
		"action":                       {"preview-end-session"},
		"session_id":                   {strconv.FormatInt(competitionID, 10)},
		"expected_live_state_revision": {"1"},
	})
	for _, want := range []string{
		"End Competition Preview",
		"Deferred Entries will become Not Presented",
		names[int(firstDeferredID)-1],
		names[int(secondDeferredID)-1],
		`name="action" value="end-session"`,
		`name="deferred_entries_fingerprint"`,
	} {
		if endPreview.status != http.StatusOK || !strings.Contains(endPreview.body, want) {
			t.Fatalf("browser Competition End Preview lacks %q: %d %q", want, endPreview.status, endPreview.body)
		}
	}
	endFormStart := strings.Index(endPreview.body, `name="action" value="end-session"`)
	endFormEnd := strings.Index(endPreview.body[endFormStart:], "</form>")
	end := frontendNamedValues(
		endPreview.body[endFormStart:endFormStart+endFormEnd],
		"session_id",
		"expected_live_state_revision",
		"command_id",
		"deferred_entries_fingerprint",
	)
	end.Set("csrf_token", requireFrontendCSRF(t, endPreview))
	end.Set("action", "end-session")
	unconfirmedEnd := postFrontendForm(t, operator, server.address, operationsPath, end)
	if unconfirmedEnd.status != http.StatusConflict ||
		!strings.Contains(unconfirmedEnd.body, "End Competition Preview") {
		t.Fatalf(
			"unconfirmed browser Competition End = %d %q",
			unconfirmedEnd.status,
			unconfirmedEnd.body,
		)
	}
	endConfirmationID := "end-session-" + strconv.FormatInt(competitionID, 10) +
		"-confirmed-deferred-entries"
	if regexp.MustCompile(`id="` + endConfirmationID + `"[^>]+checked`).MatchString(
		unconfirmedEnd.body,
	) {
		t.Fatalf("unconfirmed browser Competition End remained checked: %q", unconfirmedEnd.body)
	}
	assertAccessibleFormErrors(t, unconfirmedEnd, map[string]string{
		endConfirmationID: "Confirm deferred Entries.",
	})

	endFormStart = strings.Index(unconfirmedEnd.body, `name="action" value="end-session"`)
	endFormEnd = strings.Index(unconfirmedEnd.body[endFormStart:], "</form>")
	end = frontendNamedValues(
		unconfirmedEnd.body[endFormStart:endFormStart+endFormEnd],
		"session_id",
		"expected_live_state_revision",
		"command_id",
		"deferred_entries_fingerprint",
	)
	end.Set("csrf_token", requireFrontendCSRF(t, unconfirmedEnd))
	end.Set("action", "end-session")
	end.Set("deferred_entries_fingerprint", "stale-end-preview")
	end.Set("confirmed_deferred_entries", "true")
	staleEnd := postFrontendForm(t, operator, server.address, operationsPath, end)
	if staleEnd.status != http.StatusConflict ||
		!strings.Contains(staleEnd.body, "End Competition Preview") {
		t.Fatalf("stale browser Competition End = %d %q", staleEnd.status, staleEnd.body)
	}
	if regexp.MustCompile(`id="` + endConfirmationID + `"[^>]+checked`).MatchString(staleEnd.body) {
		t.Fatalf("stale browser Competition End remained checked: %q", staleEnd.body)
	}
	assertAccessibleFormErrors(t, staleEnd, map[string]string{
		endConfirmationID: "review and confirm",
	})

	endFormStart = strings.Index(staleEnd.body, `name="action" value="end-session"`)
	endFormEnd = strings.Index(staleEnd.body[endFormStart:], "</form>")
	end = frontendNamedValues(
		staleEnd.body[endFormStart:endFormStart+endFormEnd],
		"session_id",
		"expected_live_state_revision",
		"command_id",
		"deferred_entries_fingerprint",
	)
	end.Set("csrf_token", requireFrontendCSRF(t, staleEnd))
	end.Set("action", "end-session")
	end.Set("confirmed_deferred_entries", "true")
	ended := postFrontendForm(t, operator, server.address, operationsPath, end)
	if ended.status != http.StatusSeeOther {
		t.Fatalf("end browser Competition = %d %q", ended.status, ended.body)
	}

	page = getFrontendPage(t, administrator, server.address, path)
	invalidPublicMessage := strings.Repeat("p", 10001)
	invalidResolution := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":                      {requireFrontendCSRF(t, page)},
		"action":                          {"resolve-entry"},
		"command_id":                      {"browser-invalid-resolution"},
		"entry_id":                        {strconv.FormatInt(firstDeferredID, 10)},
		"expected_revision":               {frontendEntryRevision(t, page.body, int(firstDeferredID))},
		"result_disposition":              {"Withheld"},
		"crew_reason":                     {"Organizer decision"},
		"public_disqualification_message": {invalidPublicMessage},
	})
	if invalidResolution.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid Entry resolution = %d %q", invalidResolution.status, invalidResolution.body)
	}
	assertAccessibleFormErrors(t, invalidResolution, map[string]string{
		"resolve-entry-" + strconv.FormatInt(firstDeferredID, 10) +
			"-public-disqualification-message": "Enter no more than 10000 characters.",
	})
	resolved := postFrontendForm(t, administrator, server.address, path, url.Values{
		"csrf_token":         {requireFrontendCSRF(t, invalidResolution)},
		"action":             {"resolve-entry"},
		"command_id":         {"browser-resolve-corrected"},
		"entry_id":           {strconv.FormatInt(firstDeferredID, 10)},
		"expected_revision":  {frontendEntryRevision(t, invalidResolution.body, int(firstDeferredID))},
		"result_disposition": {"Withheld"},
		"crew_reason":        {"Organizer decision"},
	})
	if resolved.status != http.StatusSeeOther {
		t.Fatalf("corrected Entry resolution = %d %q", resolved.status, resolved.body)
	}

	resolutions := []struct {
		entryID     int64
		disposition string
		reason      string
		public      string
	}{
		{presentedID, "Disqualified", "Rules violation", "Disqualified after review"},
		{secondDeferredID, "Eligible", "Technical failure accepted", ""},
	}
	for index, resolution := range resolutions {
		page = getFrontendPage(t, administrator, server.address, path)
		result := postFrontendForm(t, administrator, server.address, path, url.Values{
			"csrf_token":                      {requireFrontendCSRF(t, page)},
			"action":                          {"resolve-entry"},
			"command_id":                      {"browser-resolve-" + strconv.Itoa(index)},
			"entry_id":                        {strconv.FormatInt(resolution.entryID, 10)},
			"expected_revision":               {frontendEntryRevision(t, page.body, int(resolution.entryID))},
			"result_disposition":              {resolution.disposition},
			"crew_reason":                     {resolution.reason},
			"public_disqualification_message": {resolution.public},
		})
		if result.status != http.StatusSeeOther {
			t.Fatalf(
				"resolve browser Entry %d as %s = %d %q",
				resolution.entryID, resolution.disposition, result.status, result.body,
			)
		}
	}
	page = getFrontendPage(t, administrator, server.address, path)
	for _, want := range []string{"Result: Withheld", "Result: Disqualified", "Result: Eligible"} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("resolved browser Entries lack %q: %q", want, page.body)
		}
	}
	public := getFrontendPage(
		t,
		authenticatedClient(t),
		server.publicAddress,
		"/events/beamconf-2099/schedule/sessions/"+strconv.FormatInt(competitionID, 10),
	)
	withheldName := names[firstDeferredID-1]
	if strings.Contains(public.body, withheldName) ||
		strings.Contains(public.body, "Organizer decision") ||
		strings.Contains(public.body, "Encoder unavailable") ||
		strings.Contains(public.body, "private ") ||
		!strings.Contains(public.body, "Disqualified after review") {
		t.Fatalf("public Competition resolution projection = %d %q", public.status, public.body)
	}
	server.stop(t)
}
