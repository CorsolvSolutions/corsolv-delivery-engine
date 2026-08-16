#Requires -Version 7.0
Set-StrictMode -Version Latest

<#
CorsolvControllerResult - the producing half of the controller-result contract.

A supervised task states what happened to it in a structured document, and the
run adjudicates that statement instead of the exit status the task's process
happened to leave behind (internal/unattended/controller.go). This module is
how a PowerShell-hosted task writes that statement on the Windows host this
engine's pilot runs on.

It is deliberately small. It does not decide anything: there is no disposition,
no retry policy and no gate verdict here, because all three are the run's to
decide and a second opinion in a second language is a second authority that can
disagree. What it does is refuse to write a document the run could not
adjudicate, which is the one thing worth doing on this side of the wire: an
unusable result reaches the run as an absence of knowledge and fails the task
safely rather than saying what went wrong.

The vocabulary is not hard-coded here either. It is read from
controller-result.contract.json, the same file the Go consumer's own tests are
checked against, so the two implementations cannot drift apart quietly.

The source is deliberately ASCII. PSScriptAnalyzer requires a byte-order mark
on a non-ASCII PowerShell file, and a BOM is a character nobody typed that
every diff and every other tool then has to know about.
#>

$script:ContractPath = Join-Path -Path $PSScriptRoot -ChildPath 'controller-result.contract.json'
# Initialized rather than left undeclared: Set-StrictMode makes reading an
# unset variable a runtime error, so the cache has to exist before it is empty.
$script:Contract = $null

function Get-CorsolvControllerContract {
    <#
    .SYNOPSIS
        The controller-result contract, read from the document both sides share.
    .DESCRIPTION
        The contract is read once and cached. It is the authority for which
        states exist; nothing in this module carries a second copy of them.
    .OUTPUTS
        System.Management.Automation.PSCustomObject
    #>
    [CmdletBinding()]
    [OutputType([PSCustomObject])]
    param()

    if ($null -eq $script:Contract) {
        if (-not (Test-Path -LiteralPath $script:ContractPath)) {
            throw "the controller-result contract is missing at $script:ContractPath"
        }
        $script:Contract = Get-Content -LiteralPath $script:ContractPath -Raw | ConvertFrom-Json
    }
    return $script:Contract
}

function Get-CorsolvControllerState {
    <#
    .SYNOPSIS
        The declared controller states, from the contract.
    .OUTPUTS
        System.String[]
    #>
    [CmdletBinding()]
    [OutputType([string[]])]
    param()

    return [string[]](Get-CorsolvControllerContract).states
}

function Get-CorsolvControllerResultPath {
    <#
    .SYNOPSIS
        Where this task is expected to state what happened to it.
    .DESCRIPTION
        The run exports the path rather than the plan repeating it, so the file
        the task writes and the file the run reads cannot be two files.
    .OUTPUTS
        System.String
    #>
    [CmdletBinding()]
    [OutputType([string])]
    param()

    $name = (Get-CorsolvControllerContract).resultPathEnvVar
    $path = [Environment]::GetEnvironmentVariable($name)
    if ([string]::IsNullOrWhiteSpace($path)) {
        throw "$name is not set: this task was not started as a supervised task, so it has nowhere to state its outcome"
    }
    return $path
}

function New-CorsolvControllerResult {
    <#
    .SYNOPSIS
        Build a controller result.
    .DESCRIPTION
        Only fields that carry a value are emitted. An absent field and a field
        set to its zero value read the same to the consumer, and emitting the
        zero value would put noise in a document a person reads when something
        has gone wrong at three in the morning.
    .PARAMETER State
        What happened: one of the contract's declared states.
    .PARAMETER TerminalReason
        Why execution ended, in this contract's own field.
    .PARAMETER Subtype
        The same fact under the name an agent runtime reports it by. Supply
        whichever your harness produced; the consumer reads both.
    .PARAMETER Detail
        A human-readable account. The run redacts it before recording it, but
        do not put a secret here.
    .PARAMETER NumTurns
        How many turns the agent used.
    .PARAMETER IsError
        The runtime's own error flag. It corroborates the state; it never
        overrides it.
    .OUTPUTS
        System.Collections.Specialized.OrderedDictionary
    #>
    [CmdletBinding()]
    [OutputType([System.Collections.Specialized.OrderedDictionary])]
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute(
        'PSUseShouldProcessForStateChangingFunctions', '',
        Justification = 'It builds an in-memory object and changes nothing. Write-CorsolvControllerResult is the function that touches the filesystem, and that one supports ShouldProcess.')]
    param(
        [Parameter(Mandatory)]
        [string] $State,

        [string] $TerminalReason,
        [string] $Subtype,
        [string] $Detail,
        [int] $NumTurns,
        [switch] $IsError
    )

    $normalized = $State.Trim().ToUpperInvariant()
    $declared = Get-CorsolvControllerState
    if ($declared -notcontains $normalized) {
        throw "state '$State' is not a declared controller state ($($declared -join ', '))"
    }

    $result = [ordered]@{ state = $normalized }
    if (-not [string]::IsNullOrWhiteSpace($TerminalReason)) {
        $result['terminal_reason'] = $TerminalReason.Trim().ToLowerInvariant()
    }
    if (-not [string]::IsNullOrWhiteSpace($Subtype)) {
        $result['subtype'] = $Subtype.Trim().ToLowerInvariant()
    }
    if ($IsError.IsPresent) {
        $result['is_error'] = $true
    }
    if (-not [string]::IsNullOrWhiteSpace($Detail)) {
        $result['detail'] = $Detail
    }
    if ($NumTurns -gt 0) {
        $result['num_turns'] = $NumTurns
    }
    return $result
}

function Test-CorsolvControllerResult {
    <#
    .SYNOPSIS
        Report whether a document is one the run could adjudicate.
    .DESCRIPTION
        This is the same question the consumer asks, and it is asked here so a
        task discovers it has written an unusable result while it is still
        running rather than by being failed for silence afterwards.
    .PARAMETER Json
        The document, as text.
    .OUTPUTS
        System.Boolean
    #>
    [CmdletBinding()]
    [OutputType([bool])]
    param(
        [Parameter(Mandatory, ValueFromPipeline)]
        [AllowEmptyString()]
        [string] $Json
    )

    process {
        if ([string]::IsNullOrWhiteSpace($Json)) {
            return $false
        }
        try {
            $parsed = $Json | ConvertFrom-Json -ErrorAction Stop
        } catch {
            return $false
        }
        if ($null -eq $parsed -or $parsed -isnot [PSCustomObject]) {
            return $false
        }
        if ($parsed.PSObject.Properties.Name -notcontains 'state') {
            return $false
        }
        $state = $parsed.state
        if ($state -isnot [string] -or [string]::IsNullOrWhiteSpace($state)) {
            return $false
        }
        return (Get-CorsolvControllerState) -contains $state.Trim().ToUpperInvariant()
    }
}

function Read-CorsolvControllerResult {
    <#
    .SYNOPSIS
        Read a controller result back, refusing anything unusable.
    .PARAMETER Path
        The document to read. Defaults to the path the run exported.
    .OUTPUTS
        System.Management.Automation.PSCustomObject
    #>
    [CmdletBinding()]
    [OutputType([PSCustomObject])]
    param(
        [string] $Path
    )

    if ([string]::IsNullOrWhiteSpace($Path)) {
        $Path = Get-CorsolvControllerResultPath
    }
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "the structured controller result is unusable: $Path was never written"
    }
    $json = Get-Content -LiteralPath $Path -Raw
    if (-not (Test-CorsolvControllerResult -Json $json)) {
        throw "the structured controller result is unusable: $Path does not state a declared outcome"
    }
    return ($json | ConvertFrom-Json)
}

function Write-CorsolvControllerResult {
    <#
    .SYNOPSIS
        State what happened to this task, where the run will read it.
    .DESCRIPTION
        The document is validated before it is written and written whole: a
        half-written result is exactly the unusable document the consumer fails
        safe on, and producing one from this side would be self-inflicted.

        UTF-8 without a byte-order mark, because a BOM is not JSON and the
        consumer would refuse the document over a character nobody typed.
    .PARAMETER Result
        The result to write, from New-CorsolvControllerResult.
    .PARAMETER Path
        Where to write it. Defaults to the path the run exported.
    .OUTPUTS
        System.String
    #>
    [CmdletBinding(SupportsShouldProcess)]
    [OutputType([string])]
    param(
        [Parameter(Mandatory)]
        [System.Collections.Specialized.OrderedDictionary] $Result,

        [string] $Path
    )

    if ([string]::IsNullOrWhiteSpace($Path)) {
        $Path = Get-CorsolvControllerResultPath
    }
    $json = $Result | ConvertTo-Json -Depth 6 -Compress
    if (-not (Test-CorsolvControllerResult -Json $json)) {
        throw "refusing to write a controller result the run could not adjudicate: $json"
    }

    $directory = Split-Path -Path $Path -Parent
    if (-not [string]::IsNullOrWhiteSpace($directory) -and -not (Test-Path -LiteralPath $directory)) {
        $null = New-Item -ItemType Directory -Path $directory -Force
    }
    if ($PSCmdlet.ShouldProcess($Path, 'write the controller result')) {
        $temporary = "$Path.tmp"
        [System.IO.File]::WriteAllText($temporary, $json, [System.Text.UTF8Encoding]::new($false))
        Move-Item -LiteralPath $temporary -Destination $Path -Force
    }
    return $Path
}

Export-ModuleMember -Function @(
    'Get-CorsolvControllerContract',
    'Get-CorsolvControllerState',
    'Get-CorsolvControllerResultPath',
    'New-CorsolvControllerResult',
    'Read-CorsolvControllerResult',
    'Test-CorsolvControllerResult',
    'Write-CorsolvControllerResult'
)
