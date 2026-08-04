package authz_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/dotwaffle/beamers/internal/authz"
)

// dynamicActionSites names every place a command builds its recorded action
// name from something other than a string literal, together with the complete
// set of names that site can produce.
//
// The scan below refuses to pass over a dynamic site it does not know, so a new
// helper that takes an action name, or a new value added to an enum a command
// concatenates into its action name, fails this test rather than reaching
// production as an unrowed action.
var dynamicActionSites = map[string][]string{
	"internal/attachments/attachments.go": {
		"ExtendReopenWindow",
		"CloseReopenWindow",
	},
	"internal/competition/competition.go": {
		"ConfigureCompetitionEntryOrder",
		"CreateCompetitionEntry",
		"UpdateCompetitionEntry",
		"CreateSubmittedCompetitionEntry",
		"UpdateSubmittedCompetitionEntry",
		"AssignCompetitionEntrySubmitter",
		"ChangeCompetitionEntryDisposition",
		"ReviewCompetitionEntry",
		"RecordCompetitionTechnicalFailure",
		"ResolveCompetitionEntry",
		"SetCompetitionEntryReleaseHold",
	},
	"internal/overrides/overrides.go": {
		"ConfigureStageMessages",
		"SendStageMessage",
		"ActivateTechnicalDifficulties",
		"ActivateUrgentNotice",
		"ActivateEmergencyAlert",
		"ClearDisplayOverride",
	},
	"internal/overrides/degraded.go": {
		"ActivateEmergencyAlert",
		"ClearDisplayOverride",
	},
	"internal/presentation/presentation.go": {
		"AssignPresentationSubmitter",
		"UpdatePresentationSubmission",
	},
	"internal/programcontrol/programcontrol.go": {
		"ChangeProgramControlClaim",
		"ChangeProgramControlRequestHandover",
		"ChangeProgramControlHandover",
		"ChangeProgramControlTakeover",
		"ChangeProgramControlDisconnect",
		"SelectProgramPreview",
		"TakeProgramOutput",
		"DeferCompetitionEntry",
		"ActOnPrizegivingResultReveal",
		"ActOnPrizegivingResultReplayReveal",
		"ActOnPrizegivingResultSkipToFinal",
		"ActOnPrizegivingResultSkipFromStage",
	},
	"internal/results/correction_application.go": {
		"SaveResultsCorrection",
		"PublishResultsCorrection",
		"ReviewResultsCorrection",
	},
	"internal/rundown/history.go": {
		"DiscardDraftChanges",
		"RevertDraftChange",
	},
	"internal/sessioncontrol/sessioncontrol.go": {
		"StartSession",
		"EndSession",
		"CancelSession",
	},
	"internal/voting/ballots.go": {
		"ConfigureCompetitionVoting",
		"OpenCompetitionVoting",
		"CloseCompetitionVoting",
		"CastCompetitionVote",
	},
}

func TestCapabilityTableIsWellFormed(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, len(authz.Table()))
	for _, row := range authz.Table() {
		if row.Action == "" {
			t.Fatal("Capability Table row has no action name")
		}
		if _, duplicate := seen[row.Action]; duplicate {
			t.Errorf("action %s has more than one Capability Table row", row.Action)
		}
		seen[row.Action] = struct{}{}

		for _, capability := range slices.Concat(row.Capabilities, row.TargetCapabilities) {
			if !authz.Known(capability) {
				t.Errorf("action %s names capability %s outside the closed enum", row.Action, capability)
			}
		}
		if len(row.Capabilities) == 0 && row.Scope != authz.ScopeNone {
			t.Errorf("action %s declares scope %s without a capability to scope", row.Action, row.Scope)
		}
		if len(row.Capabilities) > 0 && (row.Code == "" || row.ScopeCode == "") {
			t.Errorf("action %s can refuse but declares no durable rejection code", row.Action)
		}
	}
}

func TestCapabilityTableCoversEveryCommandAction(t *testing.T) {
	t.Parallel()

	recorded, dynamic := scanRecordedActions(t)

	for action, site := range recorded {
		if _, found := authz.Lookup(action); !found {
			t.Errorf("command action %s recorded at %s has no Capability Table row", action, site)
		}
	}
	for _, site := range dynamic {
		names, known := dynamicActionSites[site]
		if !known {
			t.Errorf(
				"%s builds its action name dynamically and is not listed in dynamicActionSites, "+
					"so its actions cannot be checked against the Capability Table", site,
			)
			continue
		}
		for _, action := range names {
			if _, found := authz.Lookup(action); !found {
				t.Errorf("command action %s recorded at %s has no Capability Table row", action, site)
			}
			recorded[action] = site
		}
	}

	for _, row := range authz.Table() {
		if _, produced := recorded[row.Action]; !produced {
			t.Errorf("Capability Table row %s declares an action no command records", row.Action)
		}
	}

	if len(recorded) != len(authz.Table()) {
		t.Errorf(
			"Capability Table has %d rows for %d recorded action names",
			len(authz.Table()), len(recorded),
		)
	}
}

// scanRecordedActions reads every non-test source file for store.CommandIdentity
// literals and returns the action names they record, plus the repository-relative
// paths of the files whose action names are not string literals.
func scanRecordedActions(t *testing.T) (map[string]string, []string) {
	t.Helper()

	root := repositoryRoot(t)
	recorded := make(map[string]string)
	dynamicSites := make(map[string]struct{})

	fileSet := token.NewFileSet()
	walkErr := filepath.WalkDir(
		filepath.Join(root, "internal"),
		func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			parsed, parseErr := parser.ParseFile(fileSet, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			relative = filepath.ToSlash(relative)
			ast.Inspect(parsed, func(node ast.Node) bool {
				action, dynamic, ok := commandIdentityAction(node)
				switch {
				case !ok:
				case dynamic:
					dynamicSites[relative] = struct{}{}
				default:
					recorded[action] = relative
				}
				return true
			})
			return nil
		},
	)
	if walkErr != nil {
		t.Fatalf("scan command action names: %v", walkErr)
	}

	sites := make([]string, 0, len(dynamicSites))
	for site := range dynamicSites {
		sites = append(sites, site)
	}
	sort.Strings(sites)
	return recorded, sites
}

// commandIdentityAction reports the Action field of a store.CommandIdentity
// composite literal, and whether that field is built from something other than a
// string literal.
func commandIdentityAction(node ast.Node) (action string, dynamic, ok bool) {
	literal, isLiteral := node.(*ast.CompositeLit)
	if !isLiteral {
		return "", false, false
	}
	selector, isSelector := literal.Type.(*ast.SelectorExpr)
	if !isSelector || selector.Sel.Name != "CommandIdentity" {
		return "", false, false
	}
	for _, element := range literal.Elts {
		field, isField := element.(*ast.KeyValueExpr)
		if !isField {
			continue
		}
		key, isKey := field.Key.(*ast.Ident)
		if !isKey || key.Name != "Action" {
			continue
		}
		value, isString := field.Value.(*ast.BasicLit)
		if !isString || value.Kind != token.STRING {
			return "", true, true
		}
		unquoted, unquoteErr := strconv.Unquote(value.Value)
		if unquoteErr != nil {
			return "", true, true
		}
		return unquoted, false, true
	}
	return "", false, false
}

func repositoryRoot(t *testing.T) string {
	t.Helper()

	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(directory, "go.mod")); statErr == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("no go.mod above the test working directory")
		}
		directory = parent
	}
}
