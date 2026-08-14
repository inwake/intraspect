$ErrorActionPreference = 'Stop'

function Read-JsonRequest {
  $InputText = [Console]::In.ReadToEnd()
  if ([string]::IsNullOrWhiteSpace($InputText)) {
    throw 'Expected one JSON request on stdin.'
  }

  $InputText | ConvertFrom-Json -ErrorAction Stop
}

function Convert-OutputType {
  param(
    [Parameter(Mandatory = $true)]
    $OutputType
  )

  [pscustomobject]@{
    name = $OutputType.Name
    type = if ($null -ne $OutputType.Type) { $OutputType.Type.FullName } else { $null }
  }
}

function Convert-Parameter {
  param(
    [Parameter(Mandatory = $true)]
    $Parameter
  )

  $TypeName = $null
  if ($null -ne $Parameter.ParameterType) {
    $TypeName = $Parameter.ParameterType.FullName
  }

  [pscustomobject]@{
    name = $Parameter.Name
    type = $TypeName
    aliases = @($Parameter.Aliases)
    isMandatory = $Parameter.IsMandatory
    position = $Parameter.Position
    valueFromPipeline = $Parameter.ValueFromPipeline
    valueFromPipelineByPropertyName = $Parameter.ValueFromPipelineByPropertyName
  }
}

function Convert-ParameterSet {
  param(
    [Parameter(Mandatory = $true)]
    $ParameterSet
  )

  [pscustomobject]@{
    name = $ParameterSet.Name
    isDefault = $ParameterSet.IsDefault
    parameters = @(
      $ParameterSet.Parameters |
      Sort-Object Name |
      ForEach-Object { Convert-Parameter -Parameter $_ }
    )
  }
}

try {
  $Request = Read-JsonRequest
  $CommandName = [string]$Request.command_name

  if ([string]::IsNullOrWhiteSpace($CommandName)) {
    throw 'command_name must be a non-empty string.'
  }

  if ($CommandName.IndexOfAny([char[]]'*?[]') -ge 0) {
    throw 'command_name must be an exact command name, not a wildcard pattern.'
  }

  $Commands = @(
    Get-Command -Name $CommandName -All -ErrorAction Stop |
    Where-Object { $_.Name -eq $CommandName }
  )

  if ($Commands.Count -eq 0) {
    throw "Command not found: $CommandName"
  }

  $Command = $Commands[0]
  $ParameterSets = @($Command.ParameterSets | ForEach-Object { Convert-ParameterSet -ParameterSet $_ })
  $InputTypes = @(
    $ParameterSets |
    ForEach-Object { $_.parameters } |
    Where-Object { $_.valueFromPipeline -or $_.valueFromPipelineByPropertyName } |
    Select-Object name, type, valueFromPipeline, valueFromPipelineByPropertyName -Unique
  )
  $OutputTypes = @($Command.OutputType | ForEach-Object { Convert-OutputType -OutputType $_ })

  [pscustomobject]@{
    name = $Command.Name
    commandType = $Command.CommandType.ToString()
    moduleName = $Command.ModuleName
    source = $Command.Source
    version = if ($null -ne $Command.Version) { $Command.Version.ToString() } else { $null }
    parameterSets = $ParameterSets
    inputTypes = $InputTypes
    outputTypes = $OutputTypes
  } | ConvertTo-Json -Depth 20 -Compress
} catch {
  [Console]::Error.WriteLine($_.Exception.Message)
  exit 1
}
