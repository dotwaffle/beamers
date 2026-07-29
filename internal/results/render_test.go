package results

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRenderPublicationKeepsHTMLTextAndJSONOnOneModel(t *testing.T) {
	publication := PublicResultsPublication{
		SchemaVersion: "1",
		Event: PublicResultsEvent{
			Name: "Demo & Dance", EventLocale: "de-DE",
			ContentLanguage: "fr", Language: "fr",
		},
		Revision:    7,
		Status:      ResultsPublicationFinal,
		PublishedAt: time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC),
		Items: []PublicResultsItem{{
			Kind: ResultItemCompetition,
			Competition: &PublicCompetitionResults{
				Title: "Final <Round>",
				Placed: []PublicResultEntry{
					{Name: "Aurora", Placement: 1, Score: "12.50 pts"},
					{Name: "Borealis", Placement: 2, Score: "11.00 pts"},
					{Name: "Cygnus", Placement: 2, Score: "10.75 pts"},
				},
				Unplaced: []PublicResultEntry{{Name: "Draco"}},
				Disqualified: []PublicResultEntry{{
					Name: "Equinox", Message: "Missed checkpoint",
				}},
				Awards: []PublicResultsAward{{
					Name:       "Audience Choice",
					Recipients: []string{"Aurora"},
				}},
			},
		}},
	}
	rendered, err := RenderPublicResults(publication, DefaultResultsTextTemplate())
	if err != nil {
		t.Fatalf("render public Results: %v", err)
	}
	for _, want := range []string{
		"Aurora", "Borealis", "Cygnus", "Draco", "Equinox",
		"Audience Choice", "12.50 pts",
	} {
		if !strings.Contains(rendered.HTML, want) ||
			!strings.Contains(rendered.Text, want) ||
			!strings.Contains(rendered.JSON, want) {
			t.Fatalf("%q missing from agreeing renderings: %+v", want, rendered)
		}
	}
	if !strings.Contains(rendered.HTML, "Demo &amp; Dance") ||
		!strings.Contains(rendered.HTML, "Final &lt;Round&gt;") {
		t.Fatalf("HTML did not escape public content: %s", rendered.HTML)
	}
	for _, want := range []string{
		`<html lang="fr" data-locale="de-DE">`,
		`<meta name="viewport" content="width=device-width, initial-scale=1">`,
		`<main id="main-content" tabindex="-1">`,
		`<time datetime="2026-08-21T14:00:00Z">21 Aug 2026, 14:00 UTC</time>`,
		`href="/schedule"`,
	} {
		if !strings.Contains(rendered.HTML, want) {
			t.Fatalf("localized Results HTML missing %q: %s", want, rendered.HTML)
		}
	}
	var decoded PublicResultsPublication
	if err = json.Unmarshal([]byte(rendered.JSON), &decoded); err != nil {
		t.Fatalf("decode versioned Results JSON: %v", err)
	}
	if decoded.SchemaVersion != "1" ||
		decoded.Event.EventLocale != "de-DE" ||
		decoded.Event.ContentLanguage != "fr" ||
		decoded.Event.Language != "fr" ||
		decoded.Revision != publication.Revision ||
		len(decoded.Items) != 1 {
		t.Fatalf("decoded Results JSON = %+v", decoded)
	}

	publication.Event.EventLocale = "en-US"
	rendered, err = RenderPublicResults(publication, DefaultResultsTextTemplate())
	if err != nil {
		t.Fatalf("render en-US public Results: %v", err)
	}
	if !strings.Contains(
		rendered.HTML,
		`<time datetime="2026-08-21T14:00:00Z">Aug 21, 2026, 2:00 PM UTC</time>`,
	) {
		t.Fatalf("en-US Results time missing: %s", rendered.HTML)
	}
}

func TestRenderPublicationUsesOnlyFrozenAllowlistedTemplateState(t *testing.T) {
	publication := PublicResultsPublication{
		SchemaVersion: "1",
		Event:         PublicResultsEvent{Name: "Demo"},
		Revision:      3,
		Status:        ResultsPublicationPartial,
	}
	custom := TextTemplate{
		Revision: 9,
		Source: `*** {{ upper .Event.Name }} ***
revision={{ .Revision }}`,
	}
	rendered, err := RenderPublicResults(publication, custom)
	if err != nil {
		t.Fatalf("render customized Results text: %v", err)
	}
	if rendered.Template != custom ||
		rendered.Text != "*** DEMO ***\nrevision=3" {
		t.Fatalf("custom Results text = %+v", rendered)
	}
	for _, source := range []string{
		`{{ call .Event.Name }}`,
		`{{ readFile "/etc/passwd" }}`,
		`{{ .Missing }}`,
	} {
		_, err = RenderPublicResults(
			publication,
			TextTemplate{Revision: 1, Source: source},
		)
		if err == nil {
			t.Fatalf("unsafe or invalid template %q rendered", source)
		}
	}
}

func TestRenderPublicationFailsInsteadOfFallingBack(t *testing.T) {
	publication := PublicResultsPublication{
		SchemaVersion: "1",
		Event:         PublicResultsEvent{Name: "Demo"},
		Revision:      1,
		Status:        ResultsPublicationFinal,
	}
	_, err := RenderPublicResults(
		publication,
		TextTemplate{Revision: 4, Source: `{{ if }}`},
	)
	if err == nil {
		t.Fatal("invalid Results Text Template rendered with a fallback")
	}
}
