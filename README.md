---
`intraspect` is an MCP server that gives AI agents introspection capabilities into the PowerShell engine.
---

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
