package frontend

import (
	"bytes"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/internal/attachments"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/voting"
)

func TestOrdinaryPageSkipsRepeatedNavigation(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := Root(false, "", "", false, false, nil).Render(t.Context(), &output); err != nil {
		t.Fatalf("render root: %v", err)
	}
	page := output.String()
	skip := `<a class="skip-link" href="#main-content">Skip to main content</a>`
	if !strings.Contains(page, skip) {
		t.Errorf("root missing skip link: %s", page)
	}
	if strings.Index(page, skip) > strings.Index(page, "<header>") {
		t.Errorf("skip link follows repeated navigation: %s", page)
	}
	if !strings.Contains(page, `<main id="main-content" tabindex="-1">`) {
		t.Errorf("root missing focusable main target: %s", page)
	}
}

func TestNavigationLabelsStayOutOfHeadingHierarchy(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := EventLinks(EventContext{Name: "Revision", Slug: "revision"}).
		Render(t.Context(), &output)
	if err != nil {
		t.Fatalf("render Event navigation: %v", err)
	}
	if strings.Contains(output.String(), "<h") {
		t.Errorf("Event navigation contains document heading: %s", output.String())
	}
	if !strings.Contains(output.String(), `aria-label="Revision Event"`) {
		t.Errorf("Event navigation lacks programmatic name: %s", output.String())
	}

	output.Reset()
	err = BackstageLinks(BackstageNavigation{
		Administrator: true,
		Events:        []BackstageEvent{{Name: "Revision"}},
	}).Render(t.Context(), &output)
	if err != nil {
		t.Fatalf("render Backstage navigation: %v", err)
	}
	for _, want := range []string{
		`<section aria-label="Installation">`,
		`<section aria-label="Revision">`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("Backstage navigation lacks %q: %s", want, output.String())
		}
	}
}

func TestSelectionControlsKeepNativeSizing(t *testing.T) {
	t.Parallel()

	stylesheet, err := Asset(StylesheetPath)
	if err != nil {
		t.Fatalf("load Frontend stylesheet: %v", err)
	}
	for _, want := range []string{
		`input[type="checkbox"],`,
		`input[type="radio"]`,
		`width: auto;`,
		`min-block-size: 2.75rem;`,
	} {
		if !strings.Contains(string(stylesheet), want) {
			t.Errorf("Frontend stylesheet missing %q", want)
		}
	}
}

func TestImportProposalErrorsDescribeCheckboxGroups(t *testing.T) {
	t.Parallel()

	for _, importType := range []string{"csv", "icalendar"} {
		t.Run(importType, func(t *testing.T) {
			t.Parallel()

			page := PlanningPage{
				Event:           events.Event{EventLocale: "en"},
				SubmittedAction: importType + "-import",
				Form:            url.Values{"action": {importType + "-import"}},
				Errors:          FormErrors{{FieldID: importType + "-proposal-ids", Message: "select one"}},
			}
			if importType == "csv" {
				page.CSVPreview = &rundown.CSVImportPreview{}
			} else {
				page.ICalendarPreview = &rundown.ICalendarImportPreview{}
			}
			var output bytes.Buffer
			if err := Planning(page).Render(t.Context(), &output); err != nil {
				t.Fatalf("render planning page: %v", err)
			}
			for _, want := range []string{
				`id="` + importType + `-proposal-ids" aria-describedby="` +
					importType + `-proposal-ids-error" aria-invalid="true"`,
				`id="` + importType + `-proposal-ids-error"`,
			} {
				if !strings.Contains(output.String(), want) {
					t.Errorf("planning page lacks %q: %s", want, output.String())
				}
			}
		})
	}
}

func TestDataTablesDescribeTheirRelationships(t *testing.T) {
	t.Parallel()

	now := time.Date(2099, 8, 21, 12, 0, 0, 0, time.UTC)
	var votingKeys bytes.Buffer
	err := VotingKeys(VotingKeyPage{
		Event: events.Event{Name: "Revision", EventLocale: "en", Timezone: "UTC"},
		Keys:  []voting.Key{{ID: 1, ExpiresAt: now.Add(time.Hour)}},
		Now:   now,
	}).Render(t.Context(), &votingKeys)
	if err != nil {
		t.Fatalf("render Voting Keys: %v", err)
	}
	assertAccessibleTable(
		t,
		votingKeys.String(),
		"Issued Voting Keys and their current status",
		4,
	)

	var administration bytes.Buffer
	if err = (Administration(AdministrationPage{})).
		Render(t.Context(), &administration); err != nil {
		t.Fatalf("render administration: %v", err)
	}
	assertAccessibleTable(
		t,
		administration.String(),
		"Installation audit history",
		7,
	)

	var finalFiles bytes.Buffer
	err = FinalFiles(FinalFilesPage{
		Event: events.Event{Name: "Revision", EventLocale: "en"},
		Plan:  attachments.FinalFilesPlan{},
	}).Render(t.Context(), &finalFiles)
	if err != nil {
		t.Fatalf("render Final Files: %v", err)
	}
	assertAccessibleTable(
		t,
		finalFiles.String(),
		"Files included in this exact export preview",
		7,
	)
}

func assertAccessibleTable(t *testing.T, page, caption string, columns int) {
	t.Helper()
	if !strings.Contains(page, `<div class="table-scroll"><table><caption>`+caption) {
		t.Errorf("table missing caption or reflow container: %s", page)
	}
	if got := strings.Count(page, `<th scope="col">`); got != columns {
		t.Errorf("scoped table columns = %d; want %d: %s", got, columns, page)
	}
}
