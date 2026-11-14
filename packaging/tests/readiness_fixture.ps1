param([Parameter(Mandatory)][string]$RepositoryRoot)
$ErrorActionPreference = 'Stop'
$source = Get-Content -LiteralPath (Join-Path $RepositoryRoot 'install.ps1') -Raw
$tokens = $null
$parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseInput($source, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw ($parseErrors | Out-String) }
$definition = $ast.Find({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Wait-HeadroomReady' }, $true)
$code = $definition.Extent.Text.Replace('[System.Diagnostics.Stopwatch]::StartNew()', '$script:fixtureClock')
. ([scriptblock]::Create($code))
$corpus = Get-Content -LiteralPath (Join-Path $RepositoryRoot 'packaging/fixtures/readiness.json') -Raw | ConvertFrom-Json
$temporary = Join-Path ([IO.Path]::GetTempPath()) ('headroom-readiness-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
    $InstallRoot = $temporary
    $statePath = Join-Path $InstallRoot 'install-state.json'
    $state = @{ active_version = $corpus.version; version_path = 'versions/generation' }
    $state | ConvertTo-Json | Set-Content -LiteralPath $statePath
    $executable = [IO.Path]::GetFullPath((Join-Path $temporary 'versions/generation/bin/headroom.exe'))
    $expected = [pscustomobject]@{ nonce = $corpus.nonce; version = $corpus.version; executable = $executable }
    $path = Join-Path $temporary 'ready.json'
    $valid = @{ nonce = $corpus.nonce; version = $corpus.version; executable = $executable; pid = 42 } | ConvertTo-Json -Compress
    function Start-Sleep {
        param($Milliseconds)
        $script:fixtureClock.Elapsed = $script:fixtureClock.Elapsed.Add([TimeSpan]::FromSeconds(1))
        $script:sleepCount++
        if ($script:replacement) { [IO.File]::WriteAllText($path, $script:replacement) }
        @{ active_version = '9.9.9'; version_path = 'versions/other' } | ConvertTo-Json | Set-Content -LiteralPath $statePath
    }
    foreach ($case in $corpus.cases) {
        $raw = if ($null -ne $case.raw) { $case.raw } else { $case.marker | ConvertTo-Json -Depth 10 -Compress }
        $raw = $raw.Replace('@EXE_JSON@', (ConvertTo-Json -InputObject $executable -Compress)).Replace('@EXE@', ($executable.Replace('\', '\\')))
        [IO.File]::WriteAllText($path, $raw)
        $script:fixtureClock = [pscustomobject]@{ Elapsed = [TimeSpan]::Zero }
        $script:sleepCount = 0
        $script:replacement = $null
        $accepted = Wait-HeadroomReady $path $expected
        if ($accepted -ne $case.accept) { throw "Corpus case '$($case.name)': accepted=$accepted, expected=$($case.accept)" }
        if (-not $case.accept -and $script:sleepCount -ne 10) { throw "Case '$($case.name)' did not await the elapsed deadline: $script:sleepCount sleeps" }
    }
    foreach ($initial in @('{', $valid.Replace('Nonce-AbC', 'wrong'))) {
        [IO.File]::WriteAllText($path, $initial)
        $script:fixtureClock = [pscustomobject]@{ Elapsed = [TimeSpan]::Zero }
        $script:sleepCount = 0
        $script:replacement = $valid
        if (-not (Wait-HeadroomReady $path $expected) -or $script:sleepCount -ne 1) { throw 'Wrong/malformed then correct marker ended the wait early.' }
    }
    foreach ($case in @(
        @{ name = 'case insensitive path'; raw = $valid.Replace(($executable.Replace('\', '\\')), ($executable.ToUpperInvariant().Replace('\', '\\'))); accept = $true },
        @{ name = 'oversized'; raw = $valid + (' ' * 4096); accept = $false },
        @{ name = 'missing'; raw = $null; accept = $false }
    )) {
        if ($null -eq $case.raw) { Remove-Item -LiteralPath $path } else { [IO.File]::WriteAllText($path, $case.raw) }
        $script:fixtureClock = [pscustomobject]@{ Elapsed = [TimeSpan]::Zero }
        $script:sleepCount = 0
        $script:replacement = $null
        if ((Wait-HeadroomReady $path $expected) -ne $case.accept) { throw "Failed $($case.name)" }
    }
    if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
        $previousDirectory = [Environment]::CurrentDirectory
        try {
            [Environment]::CurrentDirectory = $temporary
            $drive = [IO.Path]::GetPathRoot($temporary).Substring(0, 2)
            foreach ($relative in @(($drive + 'versions\generation\bin\headroom.exe'), $executable.Substring(2))) {
                $marker = @{ nonce = $corpus.nonce; version = $corpus.version; executable = $relative; pid = 42 }
                [IO.File]::WriteAllText($path, ($marker | ConvertTo-Json -Compress))
                $script:fixtureClock = [pscustomobject]@{ Elapsed = [TimeSpan]::Zero }
                $script:replacement = $null
                if (Wait-HeadroomReady $path $expected) { throw 'Drive-relative or root-relative executable accepted.' }
            }
        } finally { [Environment]::CurrentDirectory = $previousDirectory }
    }
    [IO.File]::WriteAllText($path, $valid.Replace($corpus.nonce, 'wrong'))
    $script:fixtureClock = [pscustomobject]@{ Elapsed = [TimeSpan]::Zero }
    $script:sleepCount = 0
    $script:replacement = $valid
    $realCode = $definition.Extent.Text
    . ([scriptblock]::Create($realCode))
    if (-not (Wait-HeadroomReady $path $expected)) { throw 'Real Stopwatch delayed readiness failed.' }
    Write-Output 'Readiness corpus and wrong-then-correct polling passed.'
} finally {
    Remove-Item -LiteralPath $temporary -Recurse -Force
}
