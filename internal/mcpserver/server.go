package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/inwake/intraspect/internal/scripts"
	"github.com/inwake/intraspect/pkg/shell"
	"github.com/mark3labs/mcp-go/mcp"
	mcpgo "github.com/mark3labs/mcp-go/server"
)

const (
	serverName    = "intraspect"
	serverVersion = "0.1.0"
)

type runner interface {
	Run(ctx context.Context, trustedScript shell.TrustedScript, requestJSON []byte) ([]byte, error)
}

type bridgeTool struct {
	name        string
	description string
	argument    string
	script      shell.TrustedScript
}

var bridgeTools = []bridgeTool{
	{
		name:        "inspect_structure",
		description: "Parses raw PowerShell code to reveal its significant AST structure without executing it.",
		argument:    "script_content",
		script: shell.TrustedScript{
			Name:    "Get-SafeAST.ps1",
			Content: scripts.InspectStructure,
		},
	},
	{
		name:        "resolve_signature",
		description: "Retrieves official syntax, parameter sets, pipeline input types, and output types for one exact PowerShell command.",
		argument:    "command_name",
		script: shell.TrustedScript{
			Name:    "Resolve-Signature.ps1",
			Content: scripts.ResolveSignature,
		},
	},
	{
		name:        "lint_analysis",
		description: "Runs PSScriptAnalyzer static analysis on a PowerShell snippet and returns diagnostics.",
		argument:    "script_content",
		script: shell.TrustedScript{
			Name:    "Invoke-LintAnalysis.ps1",
			Content: scripts.LintAnalysis,
		},
	},
}

func New() *mcpgo.MCPServer {
	return NewWithRunner(shell.NewRunner())
}

func NewWithRunner(r runner) *mcpgo.MCPServer {
	srv := mcpgo.NewMCPServer(
		serverName,
		serverVersion,
		mcpgo.WithToolCapabilities(false),
		mcpgo.WithRecovery(),
		mcpgo.WithStrictInputSchemaDefault(),
		mcpgo.WithInputSchemaValidation(),
	)

	for _, tool := range bridgeTools {
		bridgeTool := tool
		srv.AddTool(newTool(bridgeTool), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return callBridgeTool(ctx, r, bridgeTool, request), nil
		})
	}

	return srv
}

func ServeStdio() error {
	return mcpgo.ServeStdio(New())
}

func newTool(tool bridgeTool) mcp.Tool {
	return mcp.NewTool(
		tool.name,
		mcp.WithDescription(tool.description),
		mcp.WithString(
			tool.argument,
			mcp.Required(),
			mcp.Description("Required PowerShell input for "+tool.name+"."),
		),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
}

func callBridgeTool(ctx context.Context, r runner, tool bridgeTool, request mcp.CallToolRequest) *mcp.CallToolResult {
	value, err := request.RequireString(tool.argument)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	requestJSON, err := json.Marshal(map[string]string{tool.argument: value})
	if err != nil {
		return mcp.NewToolResultError("marshal bridge request: " + err.Error())
	}

	outputJSON, err := r.Run(ctx, tool.script, requestJSON)
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}

	structured, err := decodeBridgeOutput(outputJSON)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("PowerShell bridge returned invalid JSON: %v", err))
	}

	return mcp.NewToolResultStructured(structured, string(compactJSON(outputJSON)))
}

func decodeBridgeOutput(outputJSON []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(outputJSON))
	decoder.UseNumber()

	var structured any
	if err := decoder.Decode(&structured); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("multiple JSON values")
	}
	if structured == nil {
		return nil, fmt.Errorf("empty JSON value")
	}
	return structured, nil
}

func compactJSON(input []byte) []byte {
	var output bytes.Buffer
	if err := json.Compact(&output, input); err != nil {
		return input
	}
	return output.Bytes()
}
