package agentcli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestImplementationOnlyNamesStayOutOfRootPublicAPI(t *testing.T) {
	retired := map[string]struct{}{
		"AgentDefinition":          {},
		"AgentResultContract":      {},
		"AgentResultMetadataField": {},
		"CompactionConfig":         {},
		"Definitions":              {},
		"LoggingConfig":            {},
		"ObservabilityConfig":      {},
		"ProjectConfig":            {},
		"ProviderConfig":           {},
		"ProviderModelConfig":      {},
		"ProviderType":             {},
		"ProviderTypeOpenAI":       {},
		"Skill":                    {},
		"SkillLoaderToolName":      {},
		"TaskErrorCode":            {},
		"TaskRequest":              {},
		"TaskResult":               {},
		"TaskToolName":             {},
	}

	packages, err := parser.ParseDir(token.NewFileSet(), ".", func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	root := packages["agentcli"]
	if root == nil {
		t.Fatal("root agentcli package was not parsed")
	}
	for _, file := range root.Files {
		ast.Inspect(file, func(node ast.Node) bool {
			var name string
			switch declaration := node.(type) {
			case *ast.TypeSpec:
				name = declaration.Name.Name
			case *ast.ValueSpec:
				for _, identifier := range declaration.Names {
					if _, found := retired[identifier.Name]; found {
						t.Errorf("implementation-only identifier %s is exported", identifier.Name)
					}
				}
			case *ast.FuncDecl:
				name = declaration.Name.Name
				if declaration.Recv != nil && ast.IsExported(name) && receiverTypeName(declaration.Recv.List[0].Type) == "Project" {
					t.Errorf("Project must stay opaque, but exports method %s", name)
				}
			}
			if _, found := retired[name]; found {
				t.Errorf("implementation-only identifier %s is exported", name)
			}
			return true
		})
	}
}

func receiverTypeName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return receiverTypeName(value.X)
	default:
		return ""
	}
}
