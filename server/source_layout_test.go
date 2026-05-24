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

func TestCoreGraphDoesNotWireImplicitResolver(t *testing.T) {
	assertFileExcludes(t, "../core/graph/graph.go", []string{
		"resolver/step/implicit",
		"implicit.NewResolver",
	})
}

func TestResolverCompatPackagesAreOutsideCore(t *testing.T) {
	for _, dir := range []string{
		"../core/resolver/step/implicit",
		"../core/resolver/step/hamt",
	} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s should not exist under core resolver", dir)
		}
	}
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
