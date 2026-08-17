package architecture_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestPackageBoundaries(t *testing.T) {
	root := moduleRoot(t)
	tests := []struct {
		path   string
		banned []string
	}{
		{path: "transport", banned: []string{"github.com/dewebprotocol/malt-client/application", "github.com/dewebprotocol/malt-client/unixfs", "github.com/dewebprotocol/malt-client/merkledag", "github.com/dewebprotocol/malt-client/trust"}},
		{path: "trust", banned: []string{"github.com/dewebprotocol/malt-client/application", "github.com/dewebprotocol/malt-client/transport", "github.com/dewebprotocol/malt-client/unixfs", "github.com/dewebprotocol/malt-client/merkledag"}},
		{path: "unixfs", banned: []string{"github.com/dewebprotocol/malt-client/merkledag"}},
		{path: "merkledag", banned: []string{"github.com/dewebprotocol/malt-client/transport", "github.com/dewebprotocol/malt-core/auth/proof", "github.com/dewebprotocol/malt-core/protocol"}},
		{path: "application", banned: []string{"github.com/dewebprotocol/malt-client/transport", "github.com/spf13/cobra"}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			checkImports(t, filepath.Join(root, test.path), test.banned)
		})
	}
}

func TestProductionPackagesDoNotImportEvaluation(t *testing.T) {
	root := moduleRoot(t)
	set := token.NewFileSet()
	err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative == filepath.Join("internal", "evaluation") ||
				relative == filepath.Join("tools", "evaluation") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(filePath) != ".go" || strings.HasSuffix(filePath, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(set, filePath, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			value, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if value == "github.com/dewebprotocol/malt-client/internal/evaluation" ||
				strings.HasPrefix(value, "github.com/dewebprotocol/malt-client/internal/evaluation/") {
				t.Errorf("%s imports forbidden production dependency %s", filePath, value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCommandNamespacesSeparateProductAndEvaluationBinaries(t *testing.T) {
	root := moduleRoot(t)
	productEntries, err := os.ReadDir(filepath.Join(root, "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	productCommands := make([]string, 0)
	for _, entry := range productEntries {
		if entry.IsDir() {
			productCommands = append(productCommands, entry.Name())
		}
	}
	if len(productCommands) != 1 || productCommands[0] != "malt" {
		t.Fatalf("cmd must contain only the production malt command, got %v", productCommands)
	}

	evaluationEntries, err := os.ReadDir(filepath.Join(root, "tools", "evaluation", "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluationEntries) == 0 {
		t.Fatal("evaluation command namespace is empty")
	}
	for _, entry := range evaluationEntries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "malt-eval-") {
			t.Errorf("tools/evaluation/cmd contains non-evaluation entry %q", entry.Name())
		}
	}
}

func TestPublicTransportContainsNoEvaluationControlPlane(t *testing.T) {
	checkSourceTokens(t, filepath.Join(moduleRoot(t), "transport"), []string{
		"/v1/evaluation/",
		"X-Malt-Evaluation-",
		"GetRawForLocalCIDVerification",
		"PostMerkleDAGCARRead",
		"BootstrapEvaluationObject",
	})
}

func TestGenericTransportContainsNoUnixFSPayloadBinding(t *testing.T) {
	checkSourceTokens(t, filepath.Join(moduleRoot(t), "transport"), []string{"CreatePayloadRoot", "@payload", "bafkqaaa"})
}

func TestBackupPlanCompositionLivesOutsideCommandHandlers(t *testing.T) {
	file := filepath.Join(moduleRoot(t), "cmd", "malt", "backup_plans.go")
	checkImports(t, file, []string{
		"github.com/dewebprotocol/malt-client/application/add",
		"github.com/dewebprotocol/malt-client/bucketsync",
		"github.com/dewebprotocol/malt-client/internal/keyring",
		"github.com/dewebprotocol/malt-client/unixfs",
	})
	checkSourceTokens(t, file, []string{
		"type configuredPlanRunner struct",
		"func selectBackupPlans(",
		"func planFailure(",
	})
}

func checkSourceTokens(t *testing.T, root string, forbidden []string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, token := range forbidden {
			if strings.Contains(string(data), token) {
				t.Errorf("%s contains forbidden architecture token %q", path, token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate architecture test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func checkImports(t *testing.T, root string, banned []string) {
	t.Helper()
	set := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(set, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			value, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			for _, prefix := range banned {
				if value == prefix || strings.HasPrefix(value, prefix+"/") {
					t.Errorf("%s imports forbidden dependency %s", path, value)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
