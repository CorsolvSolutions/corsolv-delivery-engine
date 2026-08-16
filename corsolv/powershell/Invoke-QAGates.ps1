#Requires -Version 7.0

<#
.SYNOPSIS
    Run the PowerShell mechanical gates and record their verdicts as QA-001
    gate evidence.

.DESCRIPTION
    Three gates, three tools, one evidence document.

      build            the PowerShell parser reads every script and module in
                       scope. A file that does not parse is a file that cannot
                       run, which is what "the packet's code builds" means for
                       a language with no compiler.
      static-analysis  PSScriptAnalyzer, at Error and Warning severity.
      unit-test        the Pester suite, which is the behavioural half.

    The evidence it writes is the QA-001 GateEvidence shape
    (internal/unattended/qa.go) and nothing else: gate, tool, tool version,
    result, the revision examined, and the argv a reader runs to get the same
    verdict again. Evidence that cannot be reproduced is testimony.

    THE REVISION IS THE POINT. Every record is bound to the exact commit the
    gate examined, taken from git at the moment the gate ran, and a working
    tree with uncommitted changes produces evidence for no revision at all -
    which the adjudicator refuses rather than accepts, because a verdict about
    a tree nobody can name certifies nothing.

    A tool that is not installed produces an 'error' verdict rather than a
    silent absence. An unrun gate is an unexamined risk, and the adjudicator
    blocks on it exactly as it blocks on a gate that was never declared.

.PARAMETER Path
    The directory of PowerShell to examine. Defaults to this script's own.

.PARAMETER EvidencePath
    Where to write the evidence document.

.PARAMETER TargetSha
    The revision the verdicts certify. Defaults to the worktree's HEAD, and is
    left empty when the worktree is dirty.

.OUTPUTS
    None. The evidence document is written to EvidencePath, and the exit status
    is zero only when every gate passed.

.EXAMPLE
    pwsh -NoProfile -File corsolv/powershell/Invoke-QAGates.ps1
#>
[CmdletBinding()]
param(
    [string] $Path = $PSScriptRoot,
    [string] $EvidencePath = (Join-Path -Path $PSScriptRoot -ChildPath 'qa-gate-evidence.json'),
    [string] $TargetSha
)

# After param(), which must be a script's first statement.
Set-StrictMode -Version Latest

function Get-ExaminedFile {
    <#
    .SYNOPSIS
        Every PowerShell file the gates examine, in a stable order.
    .PARAMETER Root
        The directory to examine.
    .OUTPUTS
        System.IO.FileInfo[]
    #>
    [CmdletBinding()]
    [OutputType([System.IO.FileInfo[]])]
    param([Parameter(Mandatory)][string] $Root)

    return @(Get-ChildItem -LiteralPath $Root -Recurse -File |
            Where-Object { $_.Extension -in @('.ps1', '.psm1', '.psd1') } |
            Sort-Object -Property FullName)
}

function Get-ToolVersion {
    <#
    .SYNOPSIS
        What a gate's tool reports about itself, or empty when it is absent.
    .PARAMETER Name
        The tool: pwsh, or an installed module.
    .OUTPUTS
        System.String
    #>
    [CmdletBinding()]
    [OutputType([string])]
    param([Parameter(Mandatory)][string] $Name)

    if ($Name -eq 'pwsh') {
        return $PSVersionTable.PSVersion.ToString()
    }
    $module = Get-Module -ListAvailable -Name $Name | Sort-Object Version -Descending | Select-Object -First 1
    if ($null -eq $module) {
        return ''
    }
    return $module.Version.ToString()
}

function Get-CertifiedRevision {
    <#
    .SYNOPSIS
        The revision a verdict taken now would certify.
    .DESCRIPTION
        A dirty worktree deliberately yields no revision: the gates would be
        examining code that no commit contains, and evidence with no target
        would otherwise certify every revision, which is precisely the failure
        QA-001's contract exists to prevent (GateEvidence.Certifies).
    .PARAMETER RepositoryPath
        A path inside the worktree to ask about.
    .OUTPUTS
        System.String
    #>
    [CmdletBinding()]
    [OutputType([string])]
    param([Parameter(Mandatory)][string] $RepositoryPath)

    $head = (& git -C $RepositoryPath rev-parse HEAD 2>$null)
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($head)) {
        return ''
    }
    $dirty = (& git -C $RepositoryPath status --porcelain 2>$null)
    if ($LASTEXITCODE -ne 0 -or -not [string]::IsNullOrWhiteSpace(($dirty -join ''))) {
        return ''
    }
    return $head.Trim()
}

function New-GateRecord {
    <#
    .SYNOPSIS
        One QA-001 GateEvidence record.
    .PARAMETER GateId
        The catalog gate this evidence is for.
    .PARAMETER TaskId
        The task whose execution produced it.
    .PARAMETER Tool
        The executable or module that produced the verdict.
    .PARAMETER ToolVersion
        What that tool reported about itself.
    .PARAMETER Result
        pass, fail or error.
    .PARAMETER ObservedAt
        When the gate ran, in RFC 3339.
    .PARAMETER TargetSha
        The exact revision examined.
    .PARAMETER Reproduce
        The argv a reader runs to get this verdict again.
    .PARAMETER Detail
        The failure evidence.
    .OUTPUTS
        System.Collections.Specialized.OrderedDictionary
    #>
    [CmdletBinding()]
    [OutputType([System.Collections.Specialized.OrderedDictionary])]
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute(
        'PSUseShouldProcessForStateChangingFunctions', '',
        Justification = 'It builds an in-memory record and changes nothing; the caller writes the document.')]
    param(
        [Parameter(Mandatory)][string] $GateId,
        [Parameter(Mandatory)][string] $TaskId,
        [Parameter(Mandatory)][string] $Tool,
        [Parameter(Mandatory)][AllowEmptyString()][string] $ToolVersion,
        [Parameter(Mandatory)][string] $Result,
        [Parameter(Mandatory)][string] $ObservedAt,
        [Parameter(Mandatory)][AllowEmptyString()][string] $TargetSha,
        [Parameter(Mandatory)][string[]] $Reproduce,
        [AllowEmptyString()][string] $Detail = ''
    )

    return [ordered]@{
        gateId      = $GateId
        taskId      = $TaskId
        tool        = $Tool
        toolVersion = $ToolVersion
        result      = $Result
        observedAt  = $ObservedAt
        targetSha   = $TargetSha
        reproduce   = $Reproduce
        detail      = $Detail
    }
}

if ([string]::IsNullOrWhiteSpace($TargetSha)) {
    $TargetSha = Get-CertifiedRevision -RepositoryPath $Path
}

$files = Get-ExaminedFile -Root $Path
$observedAt = [DateTime]::UtcNow.ToString('o')
$evidence = @()

# --- build: the parser ------------------------------------------------------

$parseProblems = @()
foreach ($file in $files) {
    $tokens = $null
    $parseErrors = $null
    $null = [System.Management.Automation.Language.Parser]::ParseFile($file.FullName, [ref]$tokens, [ref]$parseErrors)
    foreach ($parseError in $parseErrors) {
        $parseProblems += "$($file.Name):$($parseError.Extent.StartLineNumber): $($parseError.Message)"
    }
}
$evidence += New-GateRecord -GateId 'build' -TaskId 'powershell-parse' `
    -Tool 'pwsh' -ToolVersion (Get-ToolVersion -Name 'pwsh') `
    -Result $(if ($parseProblems.Count -eq 0) { 'pass' } else { 'fail' }) `
    -ObservedAt $observedAt -TargetSha $TargetSha `
    -Reproduce @('pwsh', '-NoProfile', '-File', 'corsolv/powershell/Invoke-QAGates.ps1') `
    -Detail ($parseProblems -join '; ')

# --- static-analysis: PSScriptAnalyzer --------------------------------------

$analyzerVersion = Get-ToolVersion -Name 'PSScriptAnalyzer'
if ([string]::IsNullOrWhiteSpace($analyzerVersion)) {
    $evidence += New-GateRecord -GateId 'static-analysis' -TaskId 'powershell-scriptanalyzer' `
        -Tool 'PSScriptAnalyzer' -ToolVersion '' -Result 'error' `
        -ObservedAt $observedAt -TargetSha $TargetSha `
        -Reproduce @('pwsh', '-NoProfile', '-Command', 'Install-Module PSScriptAnalyzer -Scope CurrentUser') `
        -Detail 'PSScriptAnalyzer is not installed, so nothing is known about this gate'
} else {
    Import-Module -Name PSScriptAnalyzer -Force
    $findings = @(Invoke-ScriptAnalyzer -Path $Path -Recurse -Severity @('Error', 'Warning'))
    $lines = @($findings | ForEach-Object {
            "$($_.ScriptName):$($_.Line) $($_.Severity) $($_.RuleName): $($_.Message)"
        })
    $evidence += New-GateRecord -GateId 'static-analysis' -TaskId 'powershell-scriptanalyzer' `
        -Tool 'PSScriptAnalyzer' -ToolVersion $analyzerVersion `
        -Result $(if ($lines.Count -eq 0) { 'pass' } else { 'fail' }) `
        -ObservedAt $observedAt -TargetSha $TargetSha `
        -Reproduce @('pwsh', '-NoProfile', '-Command', "Invoke-ScriptAnalyzer -Path $Path -Recurse -Severity Error,Warning") `
        -Detail ($lines -join '; ')
}

# --- unit-test: Pester ------------------------------------------------------

$pesterVersion = Get-ToolVersion -Name 'Pester'
if ([string]::IsNullOrWhiteSpace($pesterVersion)) {
    $evidence += New-GateRecord -GateId 'unit-test' -TaskId 'powershell-pester' `
        -Tool 'Pester' -ToolVersion '' -Result 'error' `
        -ObservedAt $observedAt -TargetSha $TargetSha `
        -Reproduce @('pwsh', '-NoProfile', '-Command', 'Install-Module Pester -Scope CurrentUser') `
        -Detail 'Pester is not installed, so nothing is known about this gate'
} else {
    Import-Module -Name Pester -Force
    $configuration = New-PesterConfiguration
    $configuration.Run.Path = $Path
    $configuration.Run.PassThru = $true
    $configuration.Output.Verbosity = 'Detailed'
    $run = Invoke-Pester -Configuration $configuration
    $evidence += New-GateRecord -GateId 'unit-test' -TaskId 'powershell-pester' `
        -Tool 'Pester' -ToolVersion $pesterVersion `
        -Result $(if ($run.FailedCount -eq 0 -and $run.PassedCount -gt 0) { 'pass' } else { 'fail' }) `
        -ObservedAt $observedAt -TargetSha $TargetSha `
        -Reproduce @('pwsh', '-NoProfile', '-Command', "Invoke-Pester -Path $Path") `
        -Detail "$($run.PassedCount) passed, $($run.FailedCount) failed, $($run.SkippedCount) skipped"
}

$document = [ordered]@{
    schema     = 'corsolv/qa-001/gate-evidence'
    targetSha  = $TargetSha
    observedAt = $observedAt
    examined   = @($files | ForEach-Object { $_.Name })
    gates      = $evidence
}

[System.IO.File]::WriteAllText(
    $EvidencePath,
    ($document | ConvertTo-Json -Depth 8),
    [System.Text.UTF8Encoding]::new($false))

foreach ($record in $evidence) {
    Write-Information -MessageData "$($record.gateId): $($record.result) $($record.detail)" -InformationAction Continue
}
Write-Information -MessageData "evidence written to $EvidencePath" -InformationAction Continue

if (@($evidence | Where-Object { $_.result -ne 'pass' }).Count -gt 0) {
    exit 1
}
exit 0
