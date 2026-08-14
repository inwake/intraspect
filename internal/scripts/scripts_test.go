package scripts

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inwake/intraspect/pkg/shell"
)

type astNode struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	CommandName string `json:"commandName"`
	Left        string `json:"left"`
	Value       string `json:"value"`
	Mandatory   bool   `json:"mandatory"`
	Location    *struct {
		StartLine   int `json:"startLine"`
		StartColumn int `json:"startColumn"`
		EndLine     int `json:"endLine"`
		EndColumn   int `json:"endColumn"`
	} `json:"location"`
	Parameters []struct {
		Name         string `json:"name"`
		Type         string `json:"type"`
		Mandatory    bool   `json:"mandatory"`
		DefaultValue string `json:"defaultValue"`
	} `json:"parameters"`
	Elements []string `json:"elements"`
	Body     struct {
		Statements []astNode `json:"statements"`
	} `json:"body"`
	Children []astNode `json:"children"`
}

func inspectRequest(t *testing.T, scriptContent string) []byte {
	t.Helper()
	out, err := json.Marshal(map[string]string{"script_content": scriptContent})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return out
}

func walkNodes(nodes []astNode, visit func(astNode)) {
	for _, node := range nodes {
		visit(node)
		walkNodes(node.Body.Statements, visit)
		walkNodes(node.Children, visit)
	}
}

func assertConciseBridgeError(t *testing.T, err error, wantMessage string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected bridge error")
	}
	message := err.Error()
	if !strings.Contains(message, wantMessage) {
		t.Fatalf("expected error to contain %q, got %q", wantMessage, message)
	}
	if strings.ContainsAny(message, "\r\n") {
		t.Fatalf("expected one-line bridge error, got %q", message)
	}
	for _, forbidden := range []string{".ps1", "intraspect-", "Line |", "ScriptStackTrace", "PositionMessage"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("bridge error leaked PowerShell formatting marker %q: %q", forbidden, message)
		}
	}
}

func TestInspectStructureReportsSignificantNodes(t *testing.T) {
	runner := shell.NewRunner()
	request := inspectRequest(t, "function Invoke-Test { param([string]$Name) $path = \"C:\\Temp\\$Name\"; Get-ChildItem -LiteralPath $path }\n$message = 'hello'")

	out, err := runner.Run(
		context.Background(),
		shell.TrustedScript{Name: "Get-SafeAST.ps1", Content: InspectStructure},
		request,
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var got struct {
		Type        string    `json:"type"`
		ParseErrors []any     `json:"parseErrors"`
		Nodes       []astNode `json:"nodes"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out)
	}
	if got.Type != "ScriptBlockAst" {
		t.Fatalf("type mismatch: %q", got.Type)
	}
	if len(got.ParseErrors) != 0 {
		t.Fatalf("expected no parse errors, got %#v", got.ParseErrors)
	}

	seen := map[string]bool{}
	walkNodes(got.Nodes, func(node astNode) {
		switch {
		case node.Type == "FunctionDefinitionAst" && node.Name == "Invoke-Test":
			seen["function"] = true
		case node.Type == "AssignmentStatementAst" && (node.Left == "$path" || node.Left == "$message"):
			seen["assignment"] = true
		case node.Type == "CommandAst" && node.CommandName == "Get-ChildItem":
			seen["command"] = true
		case node.Type == "StringConstantExpressionAst" && (node.Value == "C:\\Temp\\$Name" || node.Value == "hello"):
			seen["string"] = true
		}
	})
	for _, key := range []string{"function", "assignment", "command", "string"} {
		if !seen[key] {
			t.Fatalf("did not find %s node in %#v", key, got.Nodes)
		}
	}
}

func TestInspectStructureHierarchyAndSanitization(t *testing.T) {
	runner := shell.NewRunner()
	request := inspectRequest(t, `
function Invoke-Test {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory = $true)]
    [string]$Name = 'world'
  )

  $path = "C:\Temp\$Name"
  Get-ChildItem -LiteralPath $path -Filter '*.txt'
}
`)

	out, err := runner.Run(
		context.Background(),
		shell.TrustedScript{Name: "Get-SafeAST.ps1", Content: InspectStructure},
		request,
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if strings.Contains(string(out), `"Parent"`) || strings.Contains(string(out), `"Extent"`) ||
		strings.Contains(string(out), `"extent"`) {
		t.Fatalf("output serialized forbidden Parent/Extent field: %s", out)
	}
	if strings.Contains(string(out), `"text"`) {
		t.Fatalf("output duplicated full source text in location: %s", out)
	}

	var got struct {
		Nodes []astNode `json:"nodes"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].Type != "FunctionDefinitionAst" {
		t.Fatalf("expected only top-level function node, got %#v", got.Nodes)
	}

	function := got.Nodes[0]
	if len(function.Body.Statements) == 0 {
		t.Fatalf("expected function body statements")
	}
	topLevelDescendantDuplicate := false
	for _, node := range got.Nodes {
		if node.Type == "AssignmentStatementAst" || node.Type == "CommandAst" || node.Type == "StringConstantExpressionAst" {
			topLevelDescendantDuplicate = true
		}
	}
	if topLevelDescendantDuplicate {
		t.Fatalf("expected descendants to be nested, got %#v", got.Nodes)
	}

	foundMandatoryParameter := false
	for _, parameter := range function.Parameters {
		if parameter.Name == "Name" && parameter.Mandatory && parameter.Type != "" && parameter.DefaultValue == "'world'" {
			foundMandatoryParameter = true
		}
	}
	if !foundMandatoryParameter {
		t.Fatalf("expected mandatory Name parameter, got %#v", function.Parameters)
	}

	commandNameStrings := 0
	filterStrings := 0
	walkNodes(got.Nodes, func(node astNode) {
		if node.Type == "StringConstantExpressionAst" && node.Value == "Get-ChildItem" {
			commandNameStrings++
		}
		if node.Type == "StringConstantExpressionAst" && node.Value == "*.txt" {
			filterStrings++
		}
		if node.Location == nil || node.Location.StartLine == 0 || node.Location.StartColumn == 0 {
			t.Fatalf("expected compact location on node %#v", node)
		}
	})
	if commandNameStrings != 0 {
		t.Fatalf("command-name token was duplicated as string node")
	}
	if filterStrings == 0 {
		t.Fatalf("expected actual hardcoded string argument to remain")
	}
}

func TestInspectStructureFunctionParametersCoversOrdinaryAndAdvancedForms(t *testing.T) {
	runner := shell.NewRunner()
	request := inspectRequest(t, `
function Invoke-Ordinary([Parameter(Mandatory = $true)][string]$Name, [int]$Count = 3) {
  $Name
}

function Invoke-Advanced {
  [CmdletBinding()]
  param(
    [Parameter(Mandatory = $true)]
    [string]$Path = 'C:\Temp'
  )
  $Path
}
`)

	out, err := runner.Run(
		context.Background(),
		shell.TrustedScript{Name: "Get-SafeAST.ps1", Content: InspectStructure},
		request,
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var got struct {
		Nodes []astNode `json:"nodes"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out)
	}

	functions := map[string]astNode{}
	for _, node := range got.Nodes {
		if node.Type == "FunctionDefinitionAst" {
			functions[node.Name] = node
		}
	}
	ordinary, ok := functions["Invoke-Ordinary"]
	if !ok {
		t.Fatalf("expected ordinary function in %#v", got.Nodes)
	}
	advanced, ok := functions["Invoke-Advanced"]
	if !ok {
		t.Fatalf("expected advanced function in %#v", got.Nodes)
	}

	foundOrdinaryName := false
	foundOrdinaryCount := false
	for _, parameter := range ordinary.Parameters {
		if parameter.Name == "Name" && parameter.Mandatory && parameter.Type != "" {
			foundOrdinaryName = true
		}
		if parameter.Name == "Count" && !parameter.Mandatory && parameter.DefaultValue == "3" {
			foundOrdinaryCount = true
		}
	}
	if !foundOrdinaryName || !foundOrdinaryCount {
		t.Fatalf("ordinary parameters missing expected metadata: %#v", ordinary.Parameters)
	}

	foundAdvancedPath := false
	for _, parameter := range advanced.Parameters {
		if parameter.Name == "Path" && parameter.Mandatory && parameter.Type != "" && parameter.DefaultValue == "'C:\\Temp'" {
			foundAdvancedPath = true
		}
	}
	if !foundAdvancedPath {
		t.Fatalf("advanced parameters missing expected metadata: %#v", advanced.Parameters)
	}
}

func TestInspectStructureParsesButDoesNotExecuteScriptContent(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "should-not-exist.txt")
	runner := shell.NewRunner()
	request := inspectRequest(t, "New-Item -ItemType File -Path '"+strings.ReplaceAll(marker, "'", "''")+"'")

	_, err := runner.Run(
		context.Background(),
		shell.TrustedScript{Name: "Get-SafeAST.ps1", Content: InspectStructure},
		request,
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("script content appears to have executed; marker stat err=%v", err)
	}
}

func TestInspectStructureReportsParseErrors(t *testing.T) {
	runner := shell.NewRunner()
	request := inspectRequest(t, "function Broken {")

	out, err := runner.Run(
		context.Background(),
		shell.TrustedScript{Name: "Get-SafeAST.ps1", Content: InspectStructure},
		request,
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var got struct {
		ParseErrors []struct {
			Message string `json:"message"`
		} `json:"parseErrors"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out)
	}
	if len(got.ParseErrors) == 0 {
		t.Fatalf("expected parse errors, got none")
	}
}

func TestInspectStructureValidationFailureIsConcise(t *testing.T) {
	runner := shell.NewRunner()
	_, err := runner.Run(
		context.Background(),
		shell.TrustedScript{Name: "Get-SafeAST.ps1", Content: InspectStructure},
		nil,
	)
	assertConciseBridgeError(t, err, "Expected one JSON request on stdin.")
}

func TestResolveSignatureReportsParameterSetsAndTypes(t *testing.T) {
	runner := shell.NewRunner()
	out, err := runner.Run(
		context.Background(),
		shell.TrustedScript{Name: "Resolve-Signature.ps1", Content: ResolveSignature},
		[]byte(`{"command_name":"Get-ChildItem"}`),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	var got struct {
		Name          string `json:"name"`
		CommandType   string `json:"commandType"`
		ParameterSets []struct {
			Name       string `json:"name"`
			Parameters []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"parameters"`
		} `json:"parameterSets"`
		InputTypes  []any `json:"inputTypes"`
		OutputTypes []any `json:"outputTypes"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out)
	}
	if got.Name != "Get-ChildItem" {
		t.Fatalf("name mismatch: %q", got.Name)
	}
	if len(got.ParameterSets) == 0 {
		t.Fatalf("expected parameter sets")
	}
	foundPath := false
	for _, parameterSet := range got.ParameterSets {
		for _, parameter := range parameterSet.Parameters {
			if parameter.Name == "Path" && parameter.Type != "" {
				foundPath = true
			}
		}
	}
	if !foundPath {
		t.Fatalf("expected Path parameter with type in %#v", got.ParameterSets)
	}
}

func TestResolveSignatureRejectsWildcards(t *testing.T) {
	runner := shell.NewRunner()
	_, err := runner.Run(
		context.Background(),
		shell.TrustedScript{Name: "Resolve-Signature.ps1", Content: ResolveSignature},
		[]byte(`{"command_name":"Get-*"}`),
	)
	assertConciseBridgeError(t, err, "command_name must be an exact command name, not a wildcard pattern.")
}

func TestLintAnalysisMissingAnalyzerFailsHelpfullyOrReturnsDiagnostics(t *testing.T) {
	runner := shell.NewRunner()
	out, err := runner.Run(
		context.Background(),
		shell.TrustedScript{Name: "Invoke-LintAnalysis.ps1", Content: LintAnalysis},
		[]byte(`{"script_content":"Get-Process"}`),
	)
	if err != nil {
		assertConciseBridgeError(t, err, "PSScriptAnalyzer is not installed. Install the PSScriptAnalyzer module to use lint_analysis.")
		return
	}

	var got struct {
		Diagnostics []any `json:"diagnostics"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out)
	}
	if got.Diagnostics == nil {
		t.Fatalf("expected diagnostics array in %s", out)
	}
}
