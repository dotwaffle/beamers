package authz_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// replayedSites names every file allowed to call authz.Replayed, which skips
// the Capability Table for a command whose authority was already decided.
//
// Only the degraded Emergency Alert path qualifies: it decides authority in
// process memory while durable evidence is unavailable and later persists that
// decision unchanged, exactly as a Command Receipt replay does. Any other file
// reaching for it is asking to bypass the table, and fails here.
var replayedSites = []string{
	"internal/overrides/degraded.go",
}

func TestReplayedFactsStayOnTheDegradedPath(t *testing.T) {
	t.Parallel()

	found := scanCallSites(t, "Replayed")
	if !slices.Equal(found, replayedSites) {
		t.Errorf(
			"authz.Replayed is called from %v, want only %v; a new call site must be "+
				"justified as a decision already made, not as a way around the table",
			found, replayedSites,
		)
	}
}

func TestEveryCommandPlanDeclaresItsAuthorization(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	fileSet := token.NewFileSet()
	plans := 0
	walkErr := filepath.WalkDir(
		filepath.Join(root, "internal"),
		func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !isCommandSource(path) {
				return err
			}
			parsed, parseErr := parser.ParseFile(fileSet, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			relative := relativePath(t, root, path)
			ast.Inspect(parsed, func(node ast.Node) bool {
				literal, isPlan := commandPlan(node)
				if !isPlan {
					return true
				}
				plans++
				if hasField(literal, "Authorization") {
					return true
				}
				t.Errorf(
					"%s:%d builds a command plan without an Authorization, so its action "+
						"would reach apply without the Capability Table judging it",
					relative, fileSet.Position(literal.Pos()).Line,
				)
				return true
			})
			return nil
		},
	)
	if walkErr != nil {
		t.Fatalf("scan command plans: %v", walkErr)
	}
	// Guard the scan itself. If the plan literal shape ever changes, the walk
	// finds nothing and this test passes while checking nothing. The floor is
	// well under the count today and only has to prove the scan still sees
	// plans at all.
	const minimumPlans = 60
	if plans < minimumPlans {
		t.Errorf(
			"found only %d command plans, so the scan no longer recognizes them "+
				"and this test is checking nothing", plans,
		)
	}
}

// scanCallSites returns the repository-relative paths of the non-test files
// calling the named function in the authz package, sorted and deduplicated.
func scanCallSites(t *testing.T, function string) []string {
	t.Helper()

	root := repositoryRoot(t)
	sites := make(map[string]struct{})
	fileSet := token.NewFileSet()
	walkErr := filepath.WalkDir(
		filepath.Join(root, "internal"),
		func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !isCommandSource(path) {
				return err
			}
			parsed, parseErr := parser.ParseFile(fileSet, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			relative := relativePath(t, root, path)
			ast.Inspect(parsed, func(node ast.Node) bool {
				call, isCall := node.(*ast.CallExpr)
				if !isCall {
					return true
				}
				if isPackageSelector(call.Fun, "authz", function) {
					sites[relative] = struct{}{}
				}
				return true
			})
			return nil
		},
	)
	if walkErr != nil {
		t.Fatalf("scan %s call sites: %v", function, walkErr)
	}

	found := make([]string, 0, len(sites))
	for site := range sites {
		found = append(found, site)
	}
	sort.Strings(found)
	return found
}

// commandPlan returns the node as a command.Plan composite literal.
func commandPlan(node ast.Node) (*ast.CompositeLit, bool) {
	literal, isLiteral := node.(*ast.CompositeLit)
	if !isLiteral {
		return nil, false
	}
	index, isIndex := literal.Type.(*ast.IndexExpr)
	if !isIndex {
		return nil, false
	}
	return literal, isPackageSelector(index.X, "command", "Plan")
}

func isPackageSelector(expr ast.Expr, pkg, name string) bool {
	selector, isSelector := expr.(*ast.SelectorExpr)
	if !isSelector || selector.Sel.Name != name {
		return false
	}
	ident, isIdent := selector.X.(*ast.Ident)
	return isIdent && ident.Name == pkg
}

func hasField(literal *ast.CompositeLit, name string) bool {
	for _, element := range literal.Elts {
		field, isField := element.(*ast.KeyValueExpr)
		if !isField {
			continue
		}
		if key, isKey := field.Key.(*ast.Ident); isKey && key.Name == name {
			return true
		}
	}
	return false
}

func isCommandSource(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
}

func relativePath(t *testing.T, root, path string) string {
	t.Helper()

	relative, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("relative path for %s: %v", path, err)
	}
	return filepath.ToSlash(relative)
}
