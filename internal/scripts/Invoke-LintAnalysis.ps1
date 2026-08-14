$ErrorActionPreference = 'Stop'

function Read-JsonRequest {
  $InputText = [Console]::In.ReadToEnd()
  if ([string]::IsNullOrWhiteSpace($InputText)) {
    throw 'Expected one JSON request on stdin.'
  }

  $InputText | ConvertFrom-Json -ErrorAction Stop
}

function Convert-Diagnostic {
  param(
    [Parameter(Mandatory = $true)]
    $Diagnostic
  )

  [pscustomobject]@{
    ruleName = $Diagnostic.RuleName
    severity = $Diagnostic.Severity.ToString()
    message = $Diagnostic.Message
    line = $Diagnostic.Line
    column = $Diagnostic.Column
    scriptName = $Diagnostic.ScriptName
  }
}

try {
  $Request = Read-JsonRequest
  $ScriptContent = [string]$Request.script_content

  if ($null -eq (Get-Module -ListAvailable -Name PSScriptAnalyzer | Select-Object -First 1)) {
    throw 'PSScriptAnalyzer is not installed. Install the PSScriptAnalyzer module to use lint_analysis.'
  }

  Import-Module PSScriptAnalyzer -ErrorAction Stop
  $Diagnostics = @(
    Invoke-ScriptAnalyzer -ScriptDefinition $ScriptContent -ErrorAction Stop |
    ForEach-Object { Convert-Diagnostic -Diagnostic $_ }
  )

  [pscustomobject]@{
    diagnostics = $Diagnostics
  } | ConvertTo-Json -Depth 10 -Compress
} catch {
  [Console]::Error.WriteLine($_.Exception.Message)
  exit 1
}
