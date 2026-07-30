package frontend

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/dotwaffle/beamers/internal/themevalue"
)

// rootBlock captures the declarations of the first :root rule in a stylesheet.
var rootBlock = regexp.MustCompile(`(?s):root\s*\{(.*?)\}`)

// customProperty captures one custom-property declaration.
var customProperty = regexp.MustCompile(`(?m)^\s*(--[a-z0-9-]+)\s*:\s*(.+?);`)

// literalClassAttribute captures a class attribute with no templ interpolation.
var literalClassAttribute = regexp.MustCompile(`class="([^"{}]*)"`)

// TestBuiltInThemeMatchesTheBaseStylesheet keeps themevalue.DefaultConfig and the
// base stylesheet's :root block from drifting apart. Every custom property the
// built-in Theme emits must already hold that value in the base stylesheet, so
// the built-in Theme is a genuine no-op override rather than a silent
// redefinition. Properties the base stylesheet defines and the Theme does not
// are fixed design tokens and are intentionally absent here.
func TestBuiltInThemeMatchesTheBaseStylesheet(t *testing.T) {
	t.Parallel()

	stylesheet, err := Asset(StylesheetPath)
	if err != nil {
		t.Fatalf("load Frontend stylesheet: %v", err)
	}
	generated, err := themevalue.Stylesheet(themevalue.DefaultConfig())
	if err != nil {
		t.Fatalf("render built-in Theme: %v", err)
	}

	base := rootProperties(t, string(stylesheet), "base stylesheet")
	built := rootProperties(t, generated, "built-in Theme")
	if len(built) == 0 {
		t.Fatal("built-in Theme declared no custom properties")
	}

	for _, name := range sortedKeys(built) {
		value, defined := base[name]
		if !defined {
			t.Errorf(
				"built-in Theme sets %s but the base stylesheet does not declare it",
				name,
			)
			continue
		}
		if value != built[name] {
			t.Errorf(
				"%s = %q in the base stylesheet; built-in Theme emits %q",
				name,
				value,
				built[name],
			)
		}
	}
}

// TestTemplatesOnlyUseDefinedClasses catches a class that markup relies on but
// no stylesheet defines. Such a class is silently inert: text meant to be
// visually hidden stays visible, and a styled state renders as ordinary prose.
func TestTemplatesOnlyUseDefinedClasses(t *testing.T) {
	t.Parallel()

	stylesheet, err := Asset(StylesheetPath)
	if err != nil {
		t.Fatalf("load Frontend stylesheet: %v", err)
	}
	var stylesheets strings.Builder
	stylesheets.Write(stylesheet)
	for _, packaged := range []struct{ directory, name string }{
		{"../schedule", "schedule.css"},
		{"../programview", "control.css"},
	} {
		content, readErr := os.ReadFile(filepath.Join(packaged.directory, packaged.name))
		if readErr != nil {
			// A folded page stylesheet is expected to disappear; its rules move
			// into the base stylesheet, which is already covered above.
			if os.IsNotExist(readErr) {
				continue
			}
			t.Fatalf("load %s: %v", packaged.name, readErr)
		}
		stylesheets.Write(content)
	}
	defined := stylesheets.String()

	for _, directory := range []string{".", "../schedule", "../programview", "../overrides"} {
		templates, globErr := filepath.Glob(filepath.Join(directory, "*.templ"))
		if globErr != nil {
			t.Fatalf("list templates in %s: %v", directory, globErr)
		}
		for _, template := range templates {
			source, readErr := os.ReadFile(template)
			if readErr != nil {
				t.Fatalf("load %s: %v", template, readErr)
			}
			for _, class := range literalClasses(string(source)) {
				if !strings.Contains(defined, "."+class) {
					t.Errorf("%s uses class %q that no stylesheet defines", template, class)
				}
			}
		}
	}
}

func rootProperties(t *testing.T, stylesheet, description string) map[string]string {
	t.Helper()
	block := rootBlock.FindStringSubmatch(stylesheet)
	if block == nil {
		t.Fatalf("%s declares no :root block", description)
	}
	properties := map[string]string{}
	for _, declaration := range customProperty.FindAllStringSubmatch(block[1], -1) {
		properties[declaration[1]] = strings.TrimSpace(declaration[2])
	}
	return properties
}

func literalClasses(source string) []string {
	unique := map[string]struct{}{}
	for _, attribute := range literalClassAttribute.FindAllStringSubmatch(source, -1) {
		for class := range strings.FieldsSeq(attribute[1]) {
			unique[class] = struct{}{}
		}
	}
	return sortedKeys(unique)
}

func sortedKeys[Value any](values map[string]Value) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
