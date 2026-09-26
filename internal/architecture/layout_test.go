package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestImplementationLivesUnderInternal(t *testing.T) {
	root := repositoryRoot(t)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			t.Fatalf("root implementation file %s is forbidden", entry.Name())
		}
	}
	for _, required := range []string{
		"internal/adapter", "internal/application", "internal/assembly/saas", "internal/domain", "internal/infrastructure/persistence", "internal/transport/http/saas", "module",
	} {
		if info, err := os.Stat(filepath.Join(root, required)); err != nil || !info.IsDir() {
			t.Fatalf("required architecture boundary %s is missing", required)
		}
	}
	for _, forbidden := range []string{"config", "httpapi", "media", "realtime", "sqlstore", "service", "repository"} {
		if info, err := os.Stat(filepath.Join(root, forbidden)); err == nil && info.IsDir() {
			t.Fatalf("top-level implementation package %s is forbidden", forbidden)
		}
	}
}

func TestCoreDoesNotImportTransportPersistenceOrIdentity(t *testing.T) {
	root := repositoryRoot(t)
	for _, relative := range []string{"internal/domain", "internal/application"} {
		err := filepath.WalkDir(filepath.Join(root, relative), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return walkErr
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imported := range file.Imports {
				value, _ := strconv.Unquote(imported.Path.Value)
				if value == "database/sql" || value == "net/http" || strings.Contains(value, "/internal/infrastructure/") || strings.Contains(value, "/internal/transport/") || strings.Contains(value, "domainry-identity-sdk") {
					t.Errorf("%s imports forbidden dependency %s", path, value)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPersistenceUsesORMBuildersInsteadOfHandwrittenSQL(t *testing.T) {
	root := filepath.Join(repositoryRoot(t), "internal/infrastructure/persistence/database/im")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return walkErr
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, _ := strconv.Unquote(literal.Value)
			upper := strings.ToUpper(strings.TrimSpace(value))
			for _, prefix := range []string{"SELECT ", "INSERT ", "UPDATE ", "DELETE ", "CREATE ", "ALTER ", "DROP "} {
				if strings.HasPrefix(upper, prefix) {
					t.Errorf("%s contains handwritten SQL %q", path, value)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRemovedTicketAndLegacyHealthRoutesDoNotReturn(t *testing.T) {
	root := repositoryRoot(t)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return walkErr
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, forbidden := range []string{"/im/event-tickets", "/healthz", "/readyz"} {
			if strings.Contains(string(content), forbidden) {
				t.Errorf("%s contains retired route %s", path, forbidden)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCoreContainsNoProductBusinessVocabulary(t *testing.T) {
	root := repositoryRoot(t)
	forbidden := []string{
		"threadsite", "threadcustomer", "estimate_request", "estimate status",
		"im_estimate_requests", "complaint", "consultation", "reference_id",
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return walkErr
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(content))
		for _, word := range forbidden {
			if strings.Contains(lower, word) {
				t.Errorf("%s contains product business vocabulary %q", path, word)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
