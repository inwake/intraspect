[CmdletBinding()]
param(
  [Parameter()]
  [string]$BinaryPath,

  [Parameter()]
  [ValidateRange(1, 120)]
  [int]$ResponseTimeoutSeconds = 30
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = 'Stop'

$RepoRoot = Resolve-Path -LiteralPath (Join-Path -Path $PSScriptRoot -ChildPath '..')

function ConvertTo-CompactJson {
  param(
    [Parameter(Mandatory = $true)]
    [object]$Value
  )

  $Value | ConvertTo-Json -Depth 30 -Compress
}

function Get-ObjectProperty {
  param(
    [Parameter(Mandatory = $true)]
    [object]$InputObject,

    [Parameter(Mandatory = $true)]
    [string]$Name
  )

  $Property = $InputObject.PSObject.Properties[$Name]
  if ($null -eq $Property) {
    return $null
  }

  $Property.Value
}

function Get-TextContent {
  param(
    [Parameter(Mandatory = $true)]
    [object]$ToolResult
  )

  if ($null -eq $ToolResult.content -or $ToolResult.content.Count -lt 1) {
    throw 'tool result did not include text content.'
  }

  $TextItem = @($ToolResult.content | Where-Object { $_.type -eq 'text' } | Select-Object -First 1)
  if ($TextItem.Count -ne 1 -or [string]::IsNullOrWhiteSpace($TextItem[0].text)) {
    throw 'tool result did not include non-empty text content.'
  }

  $TextItem[0].text
}

function Get-StderrExcerpt {
  param(
    [Parameter(Mandatory = $true)]
    [System.Threading.Tasks.Task[string]]$StderrTask,

    [Parameter(Mandatory = $true)]
    [System.Diagnostics.Process]$Process
  )

  if (-not $Process.HasExited) {
    return ''
  }

  try {
    $Text = $StderrTask.GetAwaiter().GetResult()
  } catch {
    return ''
  }

  $Text = $Text.Trim()
  if ($Text.Length -gt 4000) {
    return $Text.Substring(0, 4000) + "`n... stderr truncated by smoke test ..."
  }

  $Text
}

function Read-McpResponse {
  param(
    [Parameter(Mandatory = $true)]
    [System.Diagnostics.Process]$Process,

    [Parameter(Mandatory = $true)]
    [System.Threading.Tasks.Task[string]]$StderrTask,

    [Parameter(Mandatory = $true)]
    [int]$ExpectedId,

    [Parameter(Mandatory = $true)]
    [int]$TimeoutSeconds
  )

  $Deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
  $LineTask = $Process.StandardOutput.ReadLineAsync()
  while ([DateTimeOffset]::UtcNow -lt $Deadline) {
    $Remaining = $Deadline - [DateTimeOffset]::UtcNow
    $WaitMilliseconds = [Math]::Max(1, [Math]::Min([int]$Remaining.TotalMilliseconds, 1000))
    if (-not $LineTask.Wait($WaitMilliseconds)) {
      if ($Process.HasExited) {
        $Stderr = Get-StderrExcerpt -StderrTask $StderrTask -Process $Process
        if ($Stderr -ne '') {
          throw "MCP child exited before response $ExpectedId. stderr:`n$Stderr"
        }
        throw "MCP child exited before response $ExpectedId."
      }
      continue
    }

    $Line = $LineTask.Result
    if ($null -eq $Line) {
      $Stderr = Get-StderrExcerpt -StderrTask $StderrTask -Process $Process
      if ($Stderr -ne '') {
        throw "MCP child closed stdout before response $ExpectedId. stderr:`n$Stderr"
      }
      throw "MCP child closed stdout before response $ExpectedId."
    }

    $Message = $Line | ConvertFrom-Json -ErrorAction Stop
    $MessageId = Get-ObjectProperty -InputObject $Message -Name 'id'
    if ($null -ne $MessageId -and [int]$MessageId -eq $ExpectedId) {
      $MessageError = Get-ObjectProperty -InputObject $Message -Name 'error'
      if ($null -ne $MessageError) {
        $ErrorText = ConvertTo-CompactJson -Value $MessageError
        throw "MCP request $ExpectedId returned protocol error: $ErrorText"
      }
      return $Message
    }

    $LineTask = $Process.StandardOutput.ReadLineAsync()
  }

  throw "Timed out waiting $TimeoutSeconds seconds for MCP response $ExpectedId."
}

function Send-McpRequest {
  param(
    [Parameter(Mandatory = $true)]
    [System.Diagnostics.Process]$Process,

    [Parameter(Mandatory = $true)]
    [object]$Request
  )

  $Process.StandardInput.WriteLine((ConvertTo-CompactJson -Value $Request))
  $Process.StandardInput.Flush()
}

function Invoke-McpRequest {
  param(
    [Parameter(Mandatory = $true)]
    [System.Diagnostics.Process]$Process,

    [Parameter(Mandatory = $true)]
    [System.Threading.Tasks.Task[string]]$StderrTask,

    [Parameter(Mandatory = $true)]
    [int]$Id,

    [Parameter(Mandatory = $true)]
    [string]$Method,

    [Parameter()]
    [object]$Params = @{},

    [Parameter(Mandatory = $true)]
    [int]$TimeoutSeconds
  )

  Send-McpRequest -Process $Process -Request ([ordered]@{
    jsonrpc = '2.0'
    id = $Id
    method = $Method
    params = $Params
  })

  Read-McpResponse `
    -Process $Process `
    -StderrTask $StderrTask `
    -ExpectedId $Id `
    -TimeoutSeconds $TimeoutSeconds
}

function Assert-Condition {
  param(
    [Parameter(Mandatory = $true)]
    [bool]$Condition,

    [Parameter(Mandatory = $true)]
    [string]$Message
  )

  if (-not $Condition) {
    throw $Message
  }
}

function Assert-ToolCallSucceeded {
  param(
    [Parameter(Mandatory = $true)]
    [object]$Response,

    [Parameter(Mandatory = $true)]
    [string]$ToolName
  )

  $Result = Get-ObjectProperty -InputObject $Response -Name 'result'
  Assert-Condition -Condition ($null -ne $Result) -Message "$ToolName response missing result."

  $IsError = Get-ObjectProperty -InputObject $Result -Name 'isError'
  Assert-Condition -Condition ($IsError -ne $true) -Message "$ToolName returned tool error: $(Get-TextContent -ToolResult $Result)"

  $Text = Get-TextContent -ToolResult $Result
  $ParsedText = $Text | ConvertFrom-Json -ErrorAction Stop
  Assert-Condition -Condition ($null -ne $ParsedText) -Message "$ToolName text content was not useful JSON."

  $StructuredContent = Get-ObjectProperty -InputObject $Result -Name 'structuredContent'
  if ($null -eq $StructuredContent) {
    throw "$ToolName response missing structuredContent."
  }
}

$Process = $null
$StderrTask = $null

try {
  $StartInfo = [System.Diagnostics.ProcessStartInfo]::new()
  $StartInfo.WorkingDirectory = $RepoRoot.Path
  $StartInfo.UseShellExecute = $false
  $StartInfo.RedirectStandardInput = $true
  $StartInfo.RedirectStandardOutput = $true
  $StartInfo.RedirectStandardError = $true
  $StartInfo.CreateNoWindow = $true

  if ([string]::IsNullOrWhiteSpace($BinaryPath)) {
    $StartInfo.FileName = 'go'
    $StartInfo.ArgumentList.Add('run')
    $StartInfo.ArgumentList.Add('./cmd/intraspect')
  } else {
    $ResolvedBinary = Resolve-Path -LiteralPath $BinaryPath
    $StartInfo.FileName = $ResolvedBinary.Path
  }

  $Process = [System.Diagnostics.Process]::new()
  $Process.StartInfo = $StartInfo

  if (-not $Process.Start()) {
    throw 'failed to start MCP child process.'
  }

  $StderrTask = $Process.StandardError.ReadToEndAsync()

  $Initialize = Invoke-McpRequest `
    -Process $Process `
    -StderrTask $StderrTask `
    -Id 1 `
    -Method 'initialize' `
    -Params ([ordered]@{
      protocolVersion = '2025-11-25'
      capabilities = @{}
      clientInfo = [ordered]@{
        name = 'intraspect-smoke'
        version = '1.0.0'
      }
    }) `
    -TimeoutSeconds $ResponseTimeoutSeconds

  $InitializeResult = Get-ObjectProperty -InputObject $Initialize -Name 'result'
  Assert-Condition -Condition ($InitializeResult.serverInfo.name -eq 'intraspect') -Message 'initialize returned unexpected server name.'
  Assert-Condition -Condition ($null -ne $InitializeResult.capabilities.tools) -Message 'initialize did not advertise tool capabilities.'

  Send-McpRequest -Process $Process -Request ([ordered]@{
    jsonrpc = '2.0'
    method = 'notifications/initialized'
    params = @{}
  })

  $ToolsList = Invoke-McpRequest `
    -Process $Process `
    -StderrTask $StderrTask `
    -Id 2 `
    -Method 'tools/list' `
    -TimeoutSeconds $ResponseTimeoutSeconds

  $ToolsListResult = Get-ObjectProperty -InputObject $ToolsList -Name 'result'
  $ToolNames = @($ToolsListResult.tools | ForEach-Object { $_.name } | Sort-Object)
  $ExpectedToolNames = @('inspect_structure', 'lint_analysis', 'resolve_signature')
  Assert-Condition -Condition (@(Compare-Object -ReferenceObject $ExpectedToolNames -DifferenceObject $ToolNames).Count -eq 0) -Message "tools/list names mismatch: $($ToolNames -join ', ')"

  $Inspect = Invoke-McpRequest `
    -Process $Process `
    -StderrTask $StderrTask `
    -Id 3 `
    -Method 'tools/call' `
    -Params ([ordered]@{
      name = 'inspect_structure'
      arguments = [ordered]@{
        script_content = "function Invoke-Test { param([string]`$Name) `$message = 'hello'; Get-Process -Name `$Name }"
      }
    }) `
    -TimeoutSeconds $ResponseTimeoutSeconds
  Assert-ToolCallSucceeded -Response $Inspect -ToolName 'inspect_structure'
  $InspectResult = Get-ObjectProperty -InputObject $Inspect -Name 'result'
  $InspectStructuredContent = Get-ObjectProperty -InputObject $InspectResult -Name 'structuredContent'
  Assert-Condition -Condition ($InspectStructuredContent.type -eq 'ScriptBlockAst') -Message 'inspect_structure structuredContent.type mismatch.'
  Assert-Condition -Condition ($InspectStructuredContent.nodes.Count -gt 0) -Message 'inspect_structure returned no significant AST nodes.'

  $Signature = Invoke-McpRequest `
    -Process $Process `
    -StderrTask $StderrTask `
    -Id 4 `
    -Method 'tools/call' `
    -Params ([ordered]@{
      name = 'resolve_signature'
      arguments = [ordered]@{
        command_name = 'Get-ChildItem'
      }
    }) `
    -TimeoutSeconds $ResponseTimeoutSeconds
  Assert-ToolCallSucceeded -Response $Signature -ToolName 'resolve_signature'
  $SignatureResult = Get-ObjectProperty -InputObject $Signature -Name 'result'
  $SignatureStructuredContent = Get-ObjectProperty -InputObject $SignatureResult -Name 'structuredContent'
  Assert-Condition -Condition ($SignatureStructuredContent.name -eq 'Get-ChildItem') -Message 'resolve_signature returned the wrong command.'
  Assert-Condition -Condition ($SignatureStructuredContent.parameterSets.Count -gt 0) -Message 'resolve_signature returned no parameter sets.'
  Assert-Condition -Condition ($null -ne $SignatureStructuredContent.inputTypes) -Message 'resolve_signature missing inputTypes.'
  Assert-Condition -Condition ($null -ne $SignatureStructuredContent.outputTypes) -Message 'resolve_signature missing outputTypes.'

  $Lint = Invoke-McpRequest `
    -Process $Process `
    -StderrTask $StderrTask `
    -Id 5 `
    -Method 'tools/call' `
    -Params ([ordered]@{
      name = 'lint_analysis'
      arguments = [ordered]@{
        script_content = 'Get-ChildItem | Write-Output'
      }
    }) `
    -TimeoutSeconds $ResponseTimeoutSeconds

  $LintResult = Get-ObjectProperty -InputObject $Lint -Name 'result'
  Assert-Condition -Condition ($null -ne $LintResult) -Message 'lint_analysis response missing result.'
  $LintIsError = Get-ObjectProperty -InputObject $LintResult -Name 'isError'
  if ($LintIsError -eq $true) {
    $LintError = Get-TextContent -ToolResult $LintResult
    Assert-Condition -Condition ($LintError -like '*PSScriptAnalyzer is not installed*') -Message "lint_analysis returned unexpected tool error: $LintError"
  } else {
    Assert-ToolCallSucceeded -Response $Lint -ToolName 'lint_analysis'
    $LintStructuredContent = Get-ObjectProperty -InputObject $LintResult -Name 'structuredContent'
    Assert-Condition -Condition ($null -ne $LintStructuredContent.diagnostics) -Message 'lint_analysis missing diagnostics array.'
  }

  Write-Output 'stdio smoke passed: initialize, notifications/initialized, tools/list, inspect_structure, resolve_signature, lint_analysis'
} finally {
  if ($null -ne $Process) {
    try {
      $Process.StandardInput.Close()
    } catch {
    }

    if (-not $Process.HasExited) {
      if (-not $Process.WaitForExit(3000)) {
        $Process.Kill($true)
        $Process.WaitForExit(3000) | Out-Null
      }
    }

    $Process.Dispose()
  }
}
