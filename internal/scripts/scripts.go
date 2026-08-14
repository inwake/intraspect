package scripts

import _ "embed"

//go:embed Get-SafeAST.ps1
var InspectStructure string

//go:embed Resolve-Signature.ps1
var ResolveSignature string

//go:embed Invoke-LintAnalysis.ps1
var LintAnalysis string
