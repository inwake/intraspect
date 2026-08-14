$ErrorActionPreference = 'Stop'

function Read-JsonRequest {
  $InputText = [Console]::In.ReadToEnd()
  if ([string]::IsNullOrWhiteSpace($InputText)) {
    throw 'Expected one JSON request on stdin.'
  }

  $InputText | ConvertFrom-Json -ErrorAction Stop
}

function Convert-Location {
  param(
    [Parameter(Mandatory = $true)]
    $Extent
  )

  [pscustomobject]@{
    startLine = $Extent.StartLineNumber
    startColumn = $Extent.StartColumnNumber
    endLine = $Extent.EndLineNumber
    endColumn = $Extent.EndColumnNumber
  }
}

function Convert-ParseError {
  param(
    [Parameter(Mandatory = $true)]
    $ErrorRecord
  )

  [pscustomobject]@{
    message = $ErrorRecord.Message
    errorId = $ErrorRecord.ErrorId
    line = $ErrorRecord.Extent.StartLineNumber
    column = $ErrorRecord.Extent.StartColumnNumber
    text = $ErrorRecord.Extent.Text
  }
}

function Convert-Parameter {
  param(
    [Parameter(Mandatory = $true)]
    [System.Management.Automation.Language.ParameterAst]
    $Parameter
  )

  $TypeName = $null
  if ($null -ne $Parameter.StaticType -and $Parameter.StaticType.FullName -ne 'System.Object') {
    $TypeName = $Parameter.StaticType.FullName
  } elseif ($null -ne $Parameter.Attributes) {
    $TypeConstraint = @(
      $Parameter.Attributes |
      Where-Object { $_ -is [System.Management.Automation.Language.TypeConstraintAst] } |
      Select-Object -First 1
    )
    if ($TypeConstraint.Count -gt 0) {
      $TypeName = $TypeConstraint[0].TypeName.FullName
    }
  }

  $Mandatory = $false
  foreach ($Attribute in @($Parameter.Attributes)) {
    if ($Attribute -isnot [System.Management.Automation.Language.AttributeAst]) {
      continue
    }
    $AttributeTypeName = $Attribute.TypeName.FullName
    if ($AttributeTypeName -ne 'Parameter' -and
      $AttributeTypeName -ne 'ParameterAttribute' -and
      $AttributeTypeName -ne 'System.Management.Automation.ParameterAttribute') {
      continue
    }
    foreach ($NamedArgument in @($Attribute.NamedArguments)) {
      if ($NamedArgument.ArgumentName -ne 'Mandatory') {
        continue
      }
      if ($NamedArgument.ArgumentOmitted) {
        $Mandatory = $true
      } elseif ($NamedArgument.Argument -is [System.Management.Automation.Language.ConstantExpressionAst]) {
        $Mandatory = [bool]$NamedArgument.Argument.Value
      } else {
        $ArgumentText = $NamedArgument.Argument.Extent.Text
        $Mandatory = $ArgumentText -eq '$true' -or $ArgumentText -eq 'true' -or $ArgumentText -eq '1'
      }
    }
  }

  [pscustomobject]@{
    name = $Parameter.Name.VariablePath.UserPath
    type = $TypeName
    mandatory = $Mandatory
    defaultValue = if ($null -ne $Parameter.DefaultValue) { $Parameter.DefaultValue.Extent.Text } else { $null }
  }
}

function Get-FunctionParameters {
  param(
    [Parameter(Mandatory = $true)]
    [System.Management.Automation.Language.FunctionDefinitionAst]
    $Function
  )

  if ($null -ne $Function.Parameters -and $Function.Parameters.Count -gt 0) {
    return @($Function.Parameters | ForEach-Object { Convert-Parameter -Parameter $_ })
  }

  if ($null -ne $Function.Body.ParamBlock) {
    return @($Function.Body.ParamBlock.Parameters | ForEach-Object { Convert-Parameter -Parameter $_ })
  }

  @()
}

function Convert-Node {
  param(
    [Parameter(Mandatory = $true)]
    [System.Management.Automation.Language.Ast]
    $Node
  )

  $Base = [ordered]@{
    type = $Node.GetType().Name
    location = Convert-Location -Extent $Node.Extent
  }

  if ($Node -is [System.Management.Automation.Language.FunctionDefinitionAst]) {
    $Base.name = $Node.Name
    $Base.parameters = @(Get-FunctionParameters -Function $Node)
    $Base.body = [ordered]@{
      statements = @()
    }
  } elseif ($Node -is [System.Management.Automation.Language.AssignmentStatementAst]) {
    $Base.left = $Node.Left.Extent.Text
    $Base.operator = $Node.Operator.ToString()
    $Base.right = $Node.Right.Extent.Text
  } elseif ($Node -is [System.Management.Automation.Language.CommandAst]) {
    $Elements = @($Node.CommandElements | ForEach-Object { $_.Extent.Text })
    $Base.commandName = $Node.GetCommandName()
    $Base.elements = $Elements
  } elseif ($Node -is [System.Management.Automation.Language.StringConstantExpressionAst]) {
    $Base.value = $Node.Value
    $Base.stringConstantType = $Node.StringConstantType.ToString()
  }

  [pscustomobject]$Base
}

function Test-SignificantNode {
  param(
    [Parameter(Mandatory = $true)]
    [System.Management.Automation.Language.Ast]
    $Node
  )

  if ($Node -is [System.Management.Automation.Language.StringConstantExpressionAst] -and
    $Node.Parent -is [System.Management.Automation.Language.CommandAst] -and
    $Node.Parent.CommandElements.Count -gt 0 -and
    [object]::ReferenceEquals($Node.Parent.CommandElements[0], $Node)) {
    return $false
  }

  $Node -is [System.Management.Automation.Language.FunctionDefinitionAst] -or
  $Node -is [System.Management.Automation.Language.AssignmentStatementAst] -or
  $Node -is [System.Management.Automation.Language.CommandAst] -or
  $Node -is [System.Management.Automation.Language.StringConstantExpressionAst]
}

function Find-SignificantAncestor {
  param(
    [Parameter(Mandatory = $true)]
    [System.Management.Automation.Language.Ast]
    $Node,

    [Parameter(Mandatory = $true)]
    [System.Collections.Generic.Dictionary[System.Management.Automation.Language.Ast, object]]
    $SignificantNodes
  )

  $Current = $Node.Parent
  while ($null -ne $Current) {
    if ($SignificantNodes.ContainsKey($Current)) {
      return $Current
    }
    $Current = $Current.Parent
  }

  $null
}

function Add-ChildNode {
  param(
    [Parameter(Mandatory = $true)]
    $ParentNode,

    [Parameter(Mandatory = $true)]
    $ChildNode
  )

  if ($ParentNode.type -eq 'FunctionDefinitionAst') {
    $ParentNode.body.statements += $ChildNode
    return
  }

  if ($null -eq $ParentNode.children) {
    Add-Member -InputObject $ParentNode -MemberType NoteProperty -Name children -Value @()
  }
  $ParentNode.children += $ChildNode
}

try {
  $Request = Read-JsonRequest
  $ScriptContent = [string]$Request.script_content

  $Tokens = $null
  $ParseErrors = $null
  $Ast = [System.Management.Automation.Language.Parser]::ParseInput(
    $ScriptContent,
    [ref]$Tokens,
    [ref]$ParseErrors
  )

  $AstNodes = @(
    $Ast.FindAll({
      param($Node)

      Test-SignificantNode -Node $Node
    }, $true) |
    Sort-Object { $_.Extent.StartOffset }
  )

  $SignificantNodes = [System.Collections.Generic.Dictionary[System.Management.Automation.Language.Ast, object]]::new()
  $SerializedNodes = [System.Collections.Generic.Dictionary[System.Management.Automation.Language.Ast, object]]::new()
  foreach ($Node in $AstNodes) {
    $SignificantNodes[$Node] = $true
    $SerializedNodes[$Node] = Convert-Node -Node $Node
  }

  $RootNodes = @()
  foreach ($Node in $AstNodes) {
    $Parent = Find-SignificantAncestor -Node $Node -SignificantNodes $SignificantNodes
    if ($null -eq $Parent) {
      $RootNodes += $SerializedNodes[$Node]
    } else {
      Add-ChildNode -ParentNode $SerializedNodes[$Parent] -ChildNode $SerializedNodes[$Node]
    }
  }

  [pscustomobject]@{
    type = 'ScriptBlockAst'
    parseErrors = @($ParseErrors | ForEach-Object { Convert-ParseError -ErrorRecord $_ })
    nodes = $RootNodes
  } | ConvertTo-Json -Depth 20 -Compress
} catch {
  [Console]::Error.WriteLine($_.Exception.Message)
  exit 1
}
