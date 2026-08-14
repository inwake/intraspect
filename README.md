---
`intraspect` is an MCP server that gives AI agents introspection capabilities into the PowerShell engine.
---

### **Intraspect Development Specification (MVP)**

**Version:** 0.1.0 (Alpha)

**Stack:** Go (Golang) Latest, PowerShell Core (pwsh) 7.6+

**Protocol:** Model Context Protocol (MCP) via cli or Stdio

This specification outlines the minimum viable product (MVP) to give an AI agent "sight" into PowerShell scripts. The goal is to move beyond text processing and enable structural analysis (AST) and runtime validation.

1. Architectural Design

The system follows a **Proxy Pattern**. The Go server acts as a translation layer, converting MCP tool requests into PowerShell commands, executing them in a transient shell, and sanitizing the output into JSON that the LLM can digest without crashing.

### **The Data Flow**

1. **Agent Request:** `call_tool("inspect_ast", { code: "Get-Process | Where Id -gt 5" })`

2. **Go Server:** Writes the code to a temp buffer or passes via Stdin to `pwsh`.

3. **PowerShell Engine:** Parses the code using `[System.Management.Automation.Language.Parser]`.

4. **The Sanitizer (Critical):** A baked-in PowerShell script strips circular references (e.g., `Parent` nodes) and serializes the tree to clean JSON.

5. **Agent Response:** Returns the structural JSON to the LLM.

1. Tool Definitions (The API Surface)

These three tools form the "Holy Trinity" of introspection for the MVP.

**Tool 1: `inspect_structure` (The Eyes)**

- **Description:** Parses raw PowerShell code to reveal its Abstract Syntax Tree (AST). Use this to understand variable scope, function nesting, and logic flow.

- **Input:** `script_content` (string)

- **Output:** JSON Object (Simplified AST).

- **Key Insight:** This allows the agent to say, "I see you defined `$MyVar` in the `Process` block, but you're trying to access it in the `Begin` block."

**Tool 2: `resolve_signature` (The Fact-Checker)**

- **Description:** Retrieves the official syntax, parameter sets, and types for a specific cmdlet. Use this to verify arguments before writing code.

- **Input:** `command_name` (string)

- **Output:** JSON Object (Parameter Sets, Input Types, Output Types).

- **Key Insight:** Prevents the "Hallucinated Parameter" problem (e.g., inventing a `-Force` switch that doesn't exist).

**Tool 3: `lint_analysis` (The Quality Gate)**

- **Description:** Runs static analysis ( `PSScriptAnalyzer`) on a snippet of code to identify security risks, deprecated commands, or syntax errors.

- **Input:** `script_content` (string)

- **Output:** JSON Array (List of Warnings/Errors with line numbers).

1. The 'Sanitizer' Logic

You cannot simply dump the AST to JSON because it contains circular references ( `Child -> Parent -> Child`). You must embed a "Sanitizer Script" inside your Go binary that shapes the data.

**The JSON Contract (Target Output):**

Your Go server should expect this structure back from PowerShell:

```json
{
  "Type": "FunctionDefinitionAst",
  "Name": "Invoke-Deploy",
  "Parameters": [
    { "Name": "ComputerName", "Type": "String", "Mandatory": true }
  ],
  "Body": {
    "Statements": [ ... ]
  }
}
```

**The PowerShell Implementation strategy:**

Do not serialize everything. Focus on **Token Types** that matter to an Agent:

- `FunctionDefinitionAst` (What does this code do?)

- `AssignmentStatementAst` (Where are variables set?)

- `CommandAst` (What external tools are called?)

- `StringConstantExpressionAst` (Hardcoded paths/URLs).

1. Implementation Plan

### **Phase 1: The Runner (Scaffolding)**

- **Goal:** A Go binary that can reliably execute a hidden `pwsh` command and capture JSON output.

- **Task:** Create `pkg/shell/runner.go`.

- **Constraint:** Use `exec.Command` with Stdin piping. Do **not** use arguments for code injection (to avoid length limits and escaping hell).

### **Phase 2: The AST Bridge (Core Value)**

- **Goal:** Implement `inspect_structure`.

- **Task:** Write `internal/scripts/Get-SafeAST.ps1`.

**Logic:**

1. Call `[Parser]::ParseInput()`.

2. Walk the tree recursively.

3. Build a `PSCustomObject` that excludes `.Parent` and `.Extent`.

4. Output via `ConvertTo-Json -Depth 10`.

### **Phase 3: The Integration**

- **Goal:** Connect to Claude Desktop or an MCP Client.

- **Task:** Wire up the `mcp-go` handlers to the Runner.

- **Verify:** Ask the agent, "Analyze this script and tell me which function is unused," and ensure it calls `inspect_structure`.

1. Future Roadmap (Post-MVP)

Once the core is stable, adding these features will make Intraspect "Pro-grade":

1. **Stateful Sessions:** Instead of just analyzing text, allow the agent to attach to a *running* PID to inspect live variables ( `Get-Variable -Scope Global`).

2. **Module Explorer:** A tool to list all available modules in the environment, helping the agent discover tools it didn't know existed.

3. **Command Expansion:** A tool to take a complex one-liner with aliases ( `gci | ? { $_.Name -like "x" }`) and expand it to full verbose syntax for readability.

## Usage Notes

### Requirements

- Go 1.26 or newer.
- PowerShell Core (`pwsh`) 7.6 or newer.
- Optional: PSScriptAnalyzer. Without it, `lint_analysis` returns an actionable MCP tool error instead of installing or changing host state.

### Build

From the repository root:

```powershell
go build ./...
```

To build a named local executable:

```powershell
New-Item -ItemType Directory -Path .\bin -Force | Out-Null
go build -o .\bin\intraspect.exe .\cmd\intraspect
```

### Stdio MCP Client Configuration

Configure a stdio MCP client to launch the compiled binary. The exact file and key names vary by client, but the command shape is:

```json
{
  "mcpServers": {
    "intraspect": {
      "command": "C:\\path\\to\\intraspect.exe",
      "args": []
    }
  }
}
```

For development, a client can also launch the server through Go:

```json
{
  "mcpServers": {
    "intraspect": {
      "command": "go",
      "args": ["-C", "C:\\path\\to\\intraspect", "run", "./cmd/intraspect"]
    }
  }
}
```

### Tools

`inspect_structure`

- Input: `{ "script_content": "..." }`
- Output: a compact JSON object with `type`, `parseErrors`, and `nodes`.
- Behavior: parses the supplied PowerShell source without executing it and returns significant AST nodes: functions, assignments, commands, and string constants.

`resolve_signature`

- Input: `{ "command_name": "Get-ChildItem" }`
- Output: a compact JSON object with command identity, `parameterSets`, `inputTypes`, and `outputTypes`.
- Behavior: resolves one exact command name. Wildcard command names are rejected.

`lint_analysis`

- Input: `{ "script_content": "..." }`
- Output: a compact JSON object with `diagnostics`.
- Behavior: runs PSScriptAnalyzer when available. If PSScriptAnalyzer is not installed, the tool returns: `PSScriptAnalyzer is not installed. Install the PSScriptAnalyzer module to use lint_analysis.`

### Runner Defaults and Limits

Each tool request starts a fresh `pwsh` process with no profile and noninteractive execution. Request JSON is sent through stdin to a trusted temporary bridge script embedded in the Go binary; supplied PowerShell source is not passed as a process argument.

Default runner limits:

- stdin: 1 MiB
- stdout: 4 MiB
- stderr: 64 KiB
- timeout: 30 seconds

### Validation

Run these checks from the repository root:

```powershell
$GoFiles = @(Get-ChildItem -LiteralPath .\cmd, .\internal, .\pkg -Recurse -Filter *.go | ForEach-Object { $_.FullName })
gofmt -w $GoFiles
go test ./...
go vet ./...
go build ./...
pwsh -NoProfile -File .\scripts\smoke-test.ps1
```

The smoke test launches `go run ./cmd/intraspect` by default and exercises MCP `initialize`, `notifications/initialized`, `tools/list`, `inspect_structure`, `resolve_signature`, and `lint_analysis`. To test a compiled binary instead:

```powershell
pwsh -NoProfile -File .\scripts\smoke-test.ps1 -BinaryPath .\bin\intraspect.exe
```
