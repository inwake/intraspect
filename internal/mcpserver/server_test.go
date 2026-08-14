package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/inwake/intraspect/pkg/shell"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcpgo "github.com/mark3labs/mcp-go/server"
)

type fakeRunner struct {
	output []byte
	err    error
}

func (r fakeRunner) Run(ctx context.Context, trustedScript shell.TrustedScript, requestJSON []byte) ([]byte, error) {
	return r.output, r.err
}

func TestInitializeAndListTools(t *testing.T) {
	c := newStartedClient(t, NewWithRunner(fakeRunner{output: []byte(`{}`)}))

	result, err := c.Initialize(t.Context(), initializeRequest())
	if err != nil {
		t.Fatalf("Initialize returned error: %v", err)
	}
	if result.ServerInfo.Name != serverName {
		t.Fatalf("server name mismatch: %q", result.ServerInfo.Name)
	}
	if result.Capabilities.Tools == nil {
		t.Fatalf("expected tool capabilities")
	}

	toolsResult, err := c.ListTools(t.Context(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if len(toolsResult.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(toolsResult.Tools))
	}

	names := make([]string, 0, len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		names = append(names, tool.Name)
		assertToolShape(t, tool)
	}
	slices.Sort(names)
	wantNames := []string{"inspect_structure", "lint_analysis", "resolve_signature"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("tool names mismatch\nwant: %#v\n got: %#v", wantNames, names)
	}
}

func TestValidInspectStructureCallReturnsTextAndStructuredContent(t *testing.T) {
	c := newStartedInitializedClient(t, New())

	result := callTool(t, c, "inspect_structure", map[string]any{
		"script_content": "function Invoke-Test { $message = 'hello'; Get-Process }",
	})
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !json.Valid([]byte(text)) || strings.Contains(text, "\n") {
		t.Fatalf("expected compact JSON text, got %q", text)
	}

	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content type = %T", result.StructuredContent)
	}
	if structured["type"] != "ScriptBlockAst" {
		t.Fatalf("structured type mismatch: %#v", structured["type"])
	}
	if !strings.Contains(text, `"Invoke-Test"`) {
		t.Fatalf("expected function name in JSON text: %s", text)
	}
}

func TestValidResolveSignatureCallReturnsTextAndStructuredContent(t *testing.T) {
	c := newStartedInitializedClient(t, New())

	result := callTool(t, c, "resolve_signature", map[string]any{
		"command_name": "Get-ChildItem",
	})
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}

	text := resultText(t, result)
	if !json.Valid([]byte(text)) || strings.Contains(text, "\n") {
		t.Fatalf("expected compact JSON text, got %q", text)
	}

	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content type = %T", result.StructuredContent)
	}
	if structured["name"] != "Get-ChildItem" {
		t.Fatalf("command name mismatch: %#v", structured["name"])
	}
	if _, ok := structured["parameterSets"].([]any); !ok {
		t.Fatalf("expected parameterSets array in %#v", structured)
	}
}

func TestInvalidInputIsToolError(t *testing.T) {
	c := newStartedInitializedClient(t, NewWithRunner(fakeRunner{output: []byte(`{}`)}))

	result := callTool(t, c, "inspect_structure", map[string]any{
		"script_content": "Get-Process",
		"extra":          true,
	})
	if !result.IsError {
		t.Fatalf("expected tool error for extra argument")
	}
	if !strings.Contains(resultText(t, result), "extra") {
		t.Fatalf("expected validation message to mention extra argument: %s", resultText(t, result))
	}
}

func TestRunnerAndBridgeFailuresAreToolErrors(t *testing.T) {
	tests := []struct {
		name       string
		runner     fakeRunner
		wantString string
	}{
		{
			name:       "runner error",
			runner:     fakeRunner{err: errors.New("PowerShell bridge failed: nope")},
			wantString: "nope",
		},
		{
			name:       "invalid json",
			runner:     fakeRunner{output: []byte(`not-json`)},
			wantString: "invalid JSON",
		},
		{
			name:       "multiple json values",
			runner:     fakeRunner{output: []byte(`{} {}`)},
			wantString: "multiple JSON values",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newStartedInitializedClient(t, NewWithRunner(tt.runner))
			result := callTool(t, c, "inspect_structure", map[string]any{
				"script_content": "Get-Process",
			})
			if !result.IsError {
				t.Fatalf("expected tool error")
			}
			if !strings.Contains(resultText(t, result), tt.wantString) {
				t.Fatalf("expected %q in %q", tt.wantString, resultText(t, result))
			}
		})
	}
}

func TestBridgeValidationErrorIsToolError(t *testing.T) {
	c := newStartedInitializedClient(t, New())

	result := callTool(t, c, "resolve_signature", map[string]any{
		"command_name": "Get-*",
	})
	if !result.IsError {
		t.Fatalf("expected tool error")
	}
	if !strings.Contains(resultText(t, result), "exact command name") {
		t.Fatalf("expected bridge error, got %q", resultText(t, result))
	}
}

func assertToolShape(t *testing.T, tool mcp.Tool) {
	t.Helper()

	if tool.Description == "" {
		t.Fatalf("%s missing description", tool.Name)
	}
	if tool.InputSchema.Type != "object" {
		t.Fatalf("%s schema type = %q", tool.Name, tool.InputSchema.Type)
	}
	if tool.InputSchema.AdditionalProperties != false {
		t.Fatalf("%s schema should be closed, got %#v", tool.Name, tool.InputSchema.AdditionalProperties)
	}
	if len(tool.InputSchema.Required) != 1 {
		t.Fatalf("%s required fields = %#v", tool.Name, tool.InputSchema.Required)
	}
	for _, field := range tool.InputSchema.Required {
		property, ok := tool.InputSchema.Properties[field].(map[string]any)
		if !ok {
			t.Fatalf("%s missing property %s", tool.Name, field)
		}
		if property["type"] != "string" {
			t.Fatalf("%s property %s type = %#v", tool.Name, field, property["type"])
		}
	}

	annotations := tool.Annotations
	if annotations.ReadOnlyHint == nil || *annotations.ReadOnlyHint != true {
		t.Fatalf("%s readOnlyHint = %#v", tool.Name, annotations.ReadOnlyHint)
	}
	if annotations.DestructiveHint == nil || *annotations.DestructiveHint != false {
		t.Fatalf("%s destructiveHint = %#v", tool.Name, annotations.DestructiveHint)
	}
	if annotations.IdempotentHint == nil || *annotations.IdempotentHint != true {
		t.Fatalf("%s idempotentHint = %#v", tool.Name, annotations.IdempotentHint)
	}
	if annotations.OpenWorldHint == nil || *annotations.OpenWorldHint != false {
		t.Fatalf("%s openWorldHint = %#v", tool.Name, annotations.OpenWorldHint)
	}
}

func newStartedInitializedClient(t *testing.T, srv *mcpgo.MCPServer) *client.Client {
	t.Helper()

	c := newStartedClient(t, srv)
	if _, err := c.Initialize(t.Context(), initializeRequest()); err != nil {
		t.Fatalf("Initialize returned error: %v", err)
	}
	return c
}

func newStartedClient(t *testing.T, mcpServer *mcpgo.MCPServer) *client.Client {
	t.Helper()

	c, err := client.NewInProcessClient(mcpServer)
	if err != nil {
		t.Fatalf("NewInProcessClient returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Close()
	})
	if err := c.Start(t.Context()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	return c
}

func initializeRequest() mcp.InitializeRequest {
	request := mcp.InitializeRequest{}
	request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	request.Params.ClientInfo = mcp.Implementation{
		Name:    "intraspect-test",
		Version: "1.0.0",
	}
	return request
}

func callTool(t *testing.T, c *client.Client, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()

	request := mcp.CallToolRequest{}
	request.Params.Name = name
	request.Params.Arguments = arguments
	result, err := c.CallTool(t.Context(), request)
	if err != nil {
		t.Fatalf("CallTool returned error: %v", err)
	}
	return result
}

func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(result.Content))
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T", result.Content[0])
	}
	return text.Text
}
