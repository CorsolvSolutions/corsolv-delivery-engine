#Requires -Version 7.0
#Requires -Modules @{ ModuleName = 'Pester'; ModuleVersion = '5.0' }

<#
Behavioural tests for the producing half of the controller-result contract.

They are checked against controller-result.contract.json, the same document the
Go consumer's own tests are checked against, so the two implementations of one
wire format cannot drift apart quietly. A fixture added to it fails on both
sides until both sides handle it.

ASCII only, for the reason given in CorsolvControllerResult.psm1.
#>

BeforeAll {
    Import-Module -Name (Join-Path -Path $PSScriptRoot -ChildPath 'CorsolvControllerResult.psm1') -Force
    $script:Contract = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'controller-result.contract.json') -Raw |
        ConvertFrom-Json
}

AfterAll {
    Remove-Module -Name 'CorsolvControllerResult' -Force -ErrorAction SilentlyContinue
}

Describe 'the shared contract' {
    It 'is the module''s only source of declared states' {
        (Get-CorsolvControllerState | Sort-Object) |
            Should -Be ([string[]]$script:Contract.states | Sort-Object)
    }

    It 'names the environment variable the run exports' {
        $script:Contract.resultPathEnvVar | Should -Be 'GC_UNATTENDED_RESULT_PATH'
    }

    It 'declares a resumable reason for each spelling of a turn cap' {
        $script:Contract.terminalReasons.resumable | Should -Contain 'max_turns'
        $script:Contract.terminalReasons.resumable | Should -Contain 'error_max_turns'
    }
}

Describe 'New-CorsolvControllerResult' {
    It 'accepts every declared state' {
        foreach ($state in $script:Contract.states) {
            (New-CorsolvControllerResult -State $state)['state'] | Should -Be $state
        }
    }

    It 'normalizes case and surrounding space, because a producer is a harness' {
        (New-CorsolvControllerResult -State '  human_blocked ')['state'] | Should -Be 'HUMAN_BLOCKED'
    }

    It 'refuses a state the run could not adjudicate' {
        { New-CorsolvControllerResult -State 'MOSTLY_DONE' } | Should -Throw '*not a declared controller state*'
    }

    It 'omits a field that carries no value' {
        $result = New-CorsolvControllerResult -State 'COMPLETE'
        @($result.Keys) | Should -Be @('state')
    }

    It 'emits the fields the consumer reads, under the names it reads them by' {
        $result = New-CorsolvControllerResult -State 'FAILED' -TerminalReason 'MAX_TURNS' -Subtype 'Error_Max_Turns' -Detail 'cut off' -NumTurns 9 -IsError
        $result['terminal_reason'] | Should -Be 'max_turns'
        $result['subtype'] | Should -Be 'error_max_turns'
        $result['is_error'] | Should -BeTrue
        $result['detail'] | Should -Be 'cut off'
        $result['num_turns'] | Should -Be 9
    }
}

Describe 'Test-CorsolvControllerResult' {
    It 'accepts every fixture the contract declares usable' {
        foreach ($fixture in $script:Contract.fixtures) {
            $json = $fixture.document | ConvertTo-Json -Depth 6 -Compress
            Test-CorsolvControllerResult -Json $json |
                Should -BeTrue -Because "fixture '$($fixture.name)' is declared usable: $($fixture.why)"
        }
    }

    It 'refuses every document the contract declares unusable' {
        foreach ($invalid in $script:Contract.invalid) {
            Test-CorsolvControllerResult -Json $invalid.raw |
                Should -BeFalse -Because "fixture '$($invalid.name)' does not say what happened"
        }
    }
}

Describe 'Write-CorsolvControllerResult' {
    BeforeEach {
        $script:StateDir = Join-Path -Path $TestDrive -ChildPath ([guid]::NewGuid().ToString())
        $script:ResultPath = Join-Path -Path $script:StateDir -ChildPath 'result.json'
    }

    It 'round-trips every declared fixture through the file the run reads' {
        foreach ($fixture in $script:Contract.fixtures) {
            $document = $fixture.document
            $names = $document.PSObject.Properties.Name
            $parameters = @{ State = $document.state }
            if ($names -contains 'terminal_reason') { $parameters['TerminalReason'] = $document.terminal_reason }
            if ($names -contains 'subtype') { $parameters['Subtype'] = $document.subtype }
            if ($names -contains 'detail') { $parameters['Detail'] = $document.detail }
            if ($names -contains 'num_turns') { $parameters['NumTurns'] = $document.num_turns }
            if ($names -contains 'is_error') { $parameters['IsError'] = [switch]$document.is_error }

            $result = New-CorsolvControllerResult @parameters
            $null = Write-CorsolvControllerResult -Result $result -Path $script:ResultPath

            $read = Read-CorsolvControllerResult -Path $script:ResultPath
            $read.state | Should -Be $document.state -Because "fixture '$($fixture.name)' must survive the wire"
            if ($names -contains 'terminal_reason') {
                $read.terminal_reason | Should -Be $document.terminal_reason
            }
            if ($names -contains 'subtype') {
                $read.subtype | Should -Be $document.subtype
            }
            if ($names -contains 'num_turns') {
                $read.num_turns | Should -Be $document.num_turns
            }
        }
    }

    It 'writes UTF-8 with no byte-order mark, because a BOM is not JSON' {
        $null = Write-CorsolvControllerResult -Result (New-CorsolvControllerResult -State 'COMPLETE') -Path $script:ResultPath
        $bytes = [System.IO.File]::ReadAllBytes($script:ResultPath)
        $bytes[0] | Should -Be ([byte]0x7B) -Because 'the first byte must be the opening brace'
    }

    It 'creates the directory the run named' {
        $nested = Join-Path -Path $script:StateDir -ChildPath 'deeper/result.json'
        $null = Write-CorsolvControllerResult -Result (New-CorsolvControllerResult -State 'CONTINUE') -Path $nested
        Test-Path -LiteralPath $nested | Should -BeTrue
    }

    It 'leaves no partial document behind when it is done' {
        $null = Write-CorsolvControllerResult -Result (New-CorsolvControllerResult -State 'COMPLETE') -Path $script:ResultPath
        Test-Path -LiteralPath "$($script:ResultPath).tmp" | Should -BeFalse
    }

    It 'takes the path from the environment the run exported' {
        $previous = $env:GC_UNATTENDED_RESULT_PATH
        try {
            $env:GC_UNATTENDED_RESULT_PATH = $script:ResultPath
            $written = Write-CorsolvControllerResult -Result (New-CorsolvControllerResult -State 'COMPLETE')
            $written | Should -Be $script:ResultPath
        } finally {
            $env:GC_UNATTENDED_RESULT_PATH = $previous
        }
    }

    It 'refuses to run unsupervised rather than writing somewhere nobody reads' {
        $previous = $env:GC_UNATTENDED_RESULT_PATH
        try {
            $env:GC_UNATTENDED_RESULT_PATH = ''
            { Write-CorsolvControllerResult -Result (New-CorsolvControllerResult -State 'COMPLETE') } |
                Should -Throw '*is not set*'
        } finally {
            $env:GC_UNATTENDED_RESULT_PATH = $previous
        }
    }
}

Describe 'Read-CorsolvControllerResult' {
    It 'treats a missing document as unusable, not as silence' {
        { Read-CorsolvControllerResult -Path (Join-Path $TestDrive 'never-written.json') } |
            Should -Throw '*never written*'
    }

    It 'refuses a document that does not state a declared outcome' {
        $path = Join-Path -Path $TestDrive -ChildPath 'unusable.json'
        foreach ($invalid in $script:Contract.invalid) {
            Set-Content -LiteralPath $path -Value $invalid.raw -NoNewline
            { Read-CorsolvControllerResult -Path $path } |
                Should -Throw '*unusable*' -Because "fixture '$($invalid.name)' says nothing the run can act on"
        }
    }
}
