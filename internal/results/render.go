package results

import (
	"bytes"
	"encoding/json"
	"errors"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
	"time"
)

var (
	// ErrResultsRendering means immutable public Results could not be rendered.
	ErrResultsRendering = errors.New("public results rendering failed")
)

// PublicResultsEvent contains immutable Event identity safe for publication.
type PublicResultsEvent struct {
	Name string `json:"name"`
}

// PublicResultEntry is one public placement, unplaced, or disqualified Entry.
type PublicResultEntry struct {
	EntryID   int    `json:"entry_id,omitempty"`
	Name      string `json:"name"`
	Placement int    `json:"placement,omitempty"`
	Score     string `json:"score,omitempty"`
	Message   string `json:"message,omitempty"`
}

// PublicResultsAward is one public Award and its resolved recipient names.
type PublicResultsAward struct {
	Key        string   `json:"key,omitempty"`
	Name       string   `json:"name"`
	Recipients []string `json:"recipients"`
}

// PublicCompetitionResults is one immutable public Competition section.
type PublicCompetitionResults struct {
	SessionID    int                  `json:"session_id"`
	Title        string               `json:"title"`
	Placed       []PublicResultEntry  `json:"placed"`
	Unplaced     []PublicResultEntry  `json:"unplaced"`
	Disqualified []PublicResultEntry  `json:"disqualified"`
	Awards       []PublicResultsAward `json:"awards"`
}

// PublicNoResults records deliberate non-publication for one Competition.
type PublicNoResults struct {
	SessionID   int    `json:"session_id"`
	Title       string `json:"title"`
	Explanation string `json:"explanation"`
}

// PublicResultsItem is one ordered released Competition result or Award.
type PublicResultsItem struct {
	Kind            ResultItemKind            `json:"kind"`
	Competition     *PublicCompetitionResults `json:"competition,omitempty"`
	NoPublicResults *PublicNoResults          `json:"no_public_results,omitempty"`
	Award           *PublicResultsAward       `json:"award,omitempty"`
}

// PublicResultsPublication is the documented immutable cross-format model.
type PublicResultsPublication struct {
	SchemaVersion string                   `json:"schema_version"`
	Event         PublicResultsEvent       `json:"event"`
	EventTitle    string                   `json:"-"`
	Revision      int                      `json:"revision"`
	Status        PublicationStatus        `json:"status"`
	PublishedAt   time.Time                `json:"published_at"`
	Correction    *PublicResultsCorrection `json:"correction,omitempty"`
	Items         []PublicResultsItem      `json:"items"`
}

// PublicResultsCorrection identifies one monotonic public correction.
type PublicResultsCorrection struct {
	PreviousRevision int       `json:"previous_revision"`
	Note             string    `json:"note,omitempty"`
	CorrectedAt      time.Time `json:"corrected_at"`
}

// RenderedPublicResults freezes every canonical public representation.
type RenderedPublicResults struct {
	Model    PublicResultsPublication
	Template TextTemplate
	HTML     string
	Text     string
	JSON     string
}

const defaultResultsTextSource = `{{ .Event.Name }} Results
{{ range .Items }}{{ with .Competition }}
{{ .Title }}
{{ range .Placed }}{{ .Placement }}. {{ .Name }}{{ with .Score }} — {{ . }}{{ end }}
{{ end }}{{ range .Unplaced }}Unplaced: {{ .Name }}
{{ end }}{{ range .Disqualified }}Disqualified: {{ .Name }}{{ with .Message }} — {{ . }}{{ end }}
{{ end }}{{ range .Awards }}Award: {{ .Name }} — {{ join .Recipients ", " }}
{{ end }}{{ end }}{{ with .NoPublicResults }}
{{ .Title }}
{{ .Explanation }}
{{ end }}{{ with .Award }}
Award: {{ .Name }} — {{ join .Recipients ", " }}
{{ end }}{{ end }}`

const publicResultsHTMLSource = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{ .Event.Name }} Results</title></head>
<body><main><h1>{{ .Event.Name }} Results</h1>
{{ with .Correction }}<aside><strong>Corrected</strong>{{ with .Note }} — {{ . }}{{ end }}</aside>{{ end }}
{{ range .Items }}{{ with .Competition }}<section><h2>{{ .Title }}</h2>
{{ with .Placed }}<h3>Placements</h3><ol>{{ range . }}<li value="{{ .Placement }}">{{ .Name }}{{ with .Score }} <span>{{ . }}</span>{{ end }}</li>{{ end }}</ol>{{ end }}
{{ with .Unplaced }}<h3>Unplaced</h3><ul>{{ range . }}<li>{{ .Name }}</li>{{ end }}</ul>{{ end }}
{{ with .Disqualified }}<h3>Disqualified</h3><ul>{{ range . }}<li>{{ .Name }}{{ with .Message }} — {{ . }}{{ end }}</li>{{ end }}</ul>{{ end }}
{{ with .Awards }}<h3>Awards</h3><dl>{{ range . }}<dt>{{ .Name }}</dt><dd>{{ join .Recipients ", " }}</dd>{{ end }}</dl>{{ end }}
</section>{{ end }}{{ with .NoPublicResults }}<section><h2>{{ .Title }}</h2><p>{{ .Explanation }}</p></section>{{ end }}
{{ with .Award }}<section><h2>{{ .Name }}</h2><p>{{ join .Recipients ", " }}</p></section>{{ end }}{{ end }}
</main></body></html>`

// DefaultResultsTextTemplate returns the built-in versioned safe template.
func DefaultResultsTextTemplate() TextTemplate {
	return TextTemplate{Revision: 1, Source: defaultResultsTextSource}
}

// RenderPublicResults renders every representation from one immutable model.
func RenderPublicResults(
	publication PublicResultsPublication,
	textTemplate TextTemplate,
) (RenderedPublicResults, error) {
	parsedText, err := parseResultsTextTemplate(textTemplate)
	if err != nil {
		return RenderedPublicResults{}, ErrResultsRendering
	}
	parsedHTML, err := htmltemplate.New("results").
		Funcs(resultsTemplateFunctions()).
		Option("missingkey=error").
		Parse(publicResultsHTMLSource)
	if err != nil {
		return RenderedPublicResults{}, ErrResultsRendering
	}
	var htmlOutput bytes.Buffer
	if err = parsedHTML.Execute(&htmlOutput, publication); err != nil {
		return RenderedPublicResults{}, ErrResultsRendering
	}
	var textOutput bytes.Buffer
	if err = parsedText.Execute(&textOutput, publication); err != nil {
		return RenderedPublicResults{}, ErrResultsRendering
	}
	text := textOutput.String()
	if publication.Correction != nil {
		notice := "Corrected"
		if publication.Correction.Note != "" {
			notice += " — " + publication.Correction.Note
		}
		text = notice + "\n" + text
	}
	jsonOutput, err := json.MarshalIndent(publication, "", "  ")
	if err != nil {
		return RenderedPublicResults{}, ErrResultsRendering
	}
	return RenderedPublicResults{
		Model: publication, Template: textTemplate,
		HTML: htmlOutput.String(), Text: text,
		JSON: string(jsonOutput),
	}, nil
}

func parseResultsTextTemplate(
	value TextTemplate,
) (*texttemplate.Template, error) {
	if !boundedResultsTextTemplate(value) {
		return nil, ErrResultsRendering
	}
	parsed, err := texttemplate.New("results").
		Funcs(resultsTemplateFunctions()).
		Option("missingkey=error").
		Parse(value.Source)
	if err != nil {
		return nil, err
	}
	for _, defined := range parsed.Templates() {
		if unsafeTemplateNode(defined.Root) {
			return nil, ErrResultsRendering
		}
	}
	return parsed, nil
}

func resultsTemplateFunctions() texttemplate.FuncMap {
	return texttemplate.FuncMap{
		"join":  strings.Join,
		"lower": strings.ToLower,
		"upper": strings.ToUpper,
	}
}
