package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerRoutesAreSplitByGraphPort(t *testing.T) {
	tests := []struct {
		file    string
		symbols []string
	}{
		{"service_graph.go", []string{"type graphService struct", "func (s *Server) graphService", "func (svc graphService) ResolveKey", "func (svc graphService) ApplyMutation"}},
		{"service_verify.go", []string{"type proofVerifier struct", "func (v proofVerifier) VerifyProofList"}},
		{"routes_write.go", []string{"func (s *Server) handleSemanticMutation", "func (s *Server) handleCreateStructure"}},
		{"routes_unixfs_compat.go", []string{"func (s *Server) handleWrite", "UnixFSWriteResponse"}},
		{"routes_resolve.go", []string{"func (s *Server) handleResolve", "func (s *Server) serveResolve"}},
		{"routes_verify.go", []string{"func (s *Server) handleVerify"}},
		{"routes_content.go", []string{"func (s *Server) handleContent", "func (s *Server) readContentPayload"}},
		{"routes_admin.go", []string{"func (s *Server) handleHealth", "func (s *Server) handleMetrics"}},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			data, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatalf("ReadFile(%s): %v", tt.file, err)
			}
			text := string(data)
			for _, symbol := range tt.symbols {
				if !strings.Contains(text, symbol) {
					t.Fatalf("%s missing %q", tt.file, symbol)
				}
			}
		})
	}
}

func TestGraphServiceStaysOnGraphPorts(t *testing.T) {
	assertFileExcludes(t, "service_graph.go", []string{
		"server  *Server",
		"server *Server",
		"statForResolvedKey",
		"PathStatResponse",
		"VerifyProofList",
		"unixfs",
		"MutationPlanForRoot",
		"AddFile",
		"AddDirectory",
	})
}

func TestGenericWriteRoutesDoNotOwnUnixFSCompatibility(t *testing.T) {
	assertFileExcludes(t, "routes_write.go", []string{
		"unixFSLayout",
		"prepareUnixFSRoot",
		"applyUnixFSLayoutMutation",
		"MutationPlanForRoot",
		"AddFile",
		"AddDirectory",
		"UnixFSWriteResponse",
	})
}

func TestServerDoesNotTranslateUnixFSMutationPlans(t *testing.T) {
	assertRepositoryExcludes(t, ".", "semanticMutationFrom"+"UnixFSPlan")
}

func TestUnixFSLayoutIsOutsideCore(t *testing.T) {
	assertRepositoryExcludes(t, "..", "core/layout/malt/"+"unixfs")
}

func TestCoreUmbrellaIsRemoved(t *testing.T) {
	if _, err := os.Stat("../core"); !os.IsNotExist(err) {
		t.Fatalf("../core should not exist after the auth/graph/runtime/storage split")
	}
}

func TestGraphDoesNotWireImplicitResolver(t *testing.T) {
	assertFileExcludes(t, "../runtime/graph/graph.go", []string{
		"resolver/step/implicit",
		"implicit.NewResolver",
	})
}

func TestResolverCompatPackagesAreOutsideCore(t *testing.T) {
	for _, dir := range []string{
		"../graph/resolver/step/implicit",
		"../graph/resolver/step/hamt",
	} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s should not exist under graph resolver", dir)
		}
	}
}

func TestAuthCoreDoesNotImportOperationalLayers(t *testing.T) {
	for _, forbidden := range []string{
		"github.com/dewebprotocol/malt/api",
		"github.com/dewebprotocol/malt/cmd",
		"github.com/dewebprotocol/malt/config",
		"github.com/dewebprotocol/malt/daemon",
		"github.com/dewebprotocol/malt/layout",
		"github.com/dewebprotocol/malt/runtime",
		"github.com/dewebprotocol/malt/sdk",
		"github.com/dewebprotocol/malt/server",
		"github.com/dewebprotocol/malt/storage",
	} {
		assertTreeExcludes(t, "../auth", forbidden)
	}
}

func TestAPIDTOsDoNotImportRuntimeMetrics(t *testing.T) {
	assertTreeExcludes(t, "../api", "github.com/dewebprotocol/malt/runtime/metrics")
}

func TestStorageDoesNotImportRuntimeOrLayouts(t *testing.T) {
	assertTreeExcludes(t, "../storage", "github.com/dewebprotocol/malt/runtime")
	assertTreeExcludes(t, "../storage", "github.com/dewebprotocol/malt/layout")
}

func TestEvalHarnessLivesUnderCmdEval(t *testing.T) {
	if _, err := os.Stat("../internal/eval"); !os.IsNotExist(err) {
		t.Fatalf("../internal/eval should not exist outside cmd/eval")
	}
	if info, err := os.Stat("../cmd/eval/internal/eval"); err != nil || !info.IsDir() {
		t.Fatalf("../cmd/eval/internal/eval should exist as an eval-local harness package")
	}
}

func TestIndexedBaselineMapLivesUnderCmdEval(t *testing.T) {
	indexedDir := filepath.Join("..", "auth", "semantic", "mapping", "indexed")
	if _, err := os.Stat(indexedDir); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist in auth semantic core", indexedDir)
	}
	if info, err := os.Stat("../cmd/eval/internal/baseline/indexedmap"); err != nil || !info.IsDir() {
		t.Fatalf("../cmd/eval/internal/baseline/indexedmap should exist as an eval-local baseline")
	}
}

func TestMerkleDAGImportLivesUnderCmdBoundary(t *testing.T) {
	legacyDir := filepath.Join("..", "internal", "merkledag"+"import")
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist outside cmd", legacyDir)
	}
	if info, err := os.Stat(filepath.Join("..", "cmd", "internal", "merkledag"+"import")); err != nil || !info.IsDir() {
		t.Fatalf("../cmd/internal/merkledagimport should exist as command-local MerkleDAG import support")
	}
	assertRepositoryExcludes(t, "..", "malt/internal/"+"merkledagimport")
}

func TestGatewayShimIsDeleted(t *testing.T) {
	removedDir := filepath.Join("..", "core", "gate"+"way")
	if _, err := os.Stat(removedDir); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist after writer owns mutations", removedDir)
	}
	assertRepositoryExcludes(t, "..", "core/"+"gate"+"way")
}

func TestLegacyUnixFSFlatBatchAPIIsDeleted(t *testing.T) {
	for _, symbol := range []string{
		"UnixFS" + "Batch" + "Request",
		"UnixFS" + "Batch" + "Entry",
		"UnixFS" + "Batch" + "Response",
		"flatUnixFS" + "BatchEntries",
	} {
		assertRepositoryExcludes(t, "..", symbol)
	}
}

func TestResolveResponseDoesNotExposeLegacyTranscriptAPI(t *testing.T) {
	assertFileExcludes(t, "../api/http/types.go", []string{
		"type Step" + "Evidence struct",
		"Transcript []Step" + "Evidence",
	})
	assertFileExcludes(t, "server.go", []string{
		"encode" + "Transcript",
		"evidence" + "Kind(",
	})
}

func TestServerStaleUnixFSArcCountersAreDeleted(t *testing.T) {
	assertFileExcludes(t, "handlers.go", []string{
		"unixFS" + "ArcCount",
	})
}

func assertFileExcludes(t *testing.T, file string, forbidden []string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", file, err)
	}
	text := string(data)
	for _, symbol := range forbidden {
		if strings.Contains(text, symbol) {
			t.Fatalf("%s should not contain %q", file, symbol)
		}
	}
}

func assertRepositoryExcludes(t *testing.T, root string, forbidden string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", root, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		assertTreeExcludes(t, filepath.Join(root, entry.Name()), forbidden)
	}
}

func assertTreeExcludes(t *testing.T, path string, forbidden string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if !info.IsDir() {
		if !strings.HasSuffix(path, ".go") {
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("%s should not contain %q", path, forbidden)
		}
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", path, err)
	}
	for _, entry := range entries {
		assertTreeExcludes(t, filepath.Join(path, entry.Name()), forbidden)
	}
}
