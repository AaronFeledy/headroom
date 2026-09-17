[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$IconPath,
    [Parameter(Mandatory)][string]$Version,
    [Parameter(Mandatory)][ValidateSet('x86_64', 'arm64')][string]$Architecture,
    [Parameter(Mandatory)][string]$OutputPath
)
$ErrorActionPreference = 'Stop'
if ($Version -notmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$') { throw 'Invalid stable package version.' }
$components = @($Matches[1], $Matches[2], $Matches[3])
foreach ($component in $components) { if ([int64]$component -gt 65534) { throw 'Version component exceeds Windows resource limits.' } }
$numericVersion = ($components -join '.') + '.0'
$target = if ($Architecture -eq 'arm64') { 'arm64' } else { 'amd64' }
$icon = (Resolve-Path -LiteralPath $IconPath).ProviderPath
$output = [IO.Path]::GetFullPath($OutputPath)
if (Test-Path -LiteralPath $output) { throw "Resource output already exists: $output" }
# Link resources before package hashing/signing; do not modify a completed Go PE.
# Pin this build-only compiler. It is not included in the installed application.
$inputFile = Join-Path ([IO.Path]::GetTempPath()) ('headroom-branding-' + [guid]::NewGuid().ToString('N') + '.json')
$iconFile = [IO.Path]::ChangeExtension($inputFile, '.ico')
$description = @{
    RT_GROUP_ICON = @{ APP = @{ '0000' = [IO.Path]::GetFileName($iconFile) } }
    RT_VERSION = @{ '#1' = @{ '0000' = @{
        fixed = @{ file_version = $numericVersion; product_version = $numericVersion }
        info = @{ '0409' = @{
            CompanyName = 'Headroom contributors'; FileDescription = 'Headroom AI usage monitor'
            FileVersion = $Version; InternalName = 'headroom'; OriginalFilename = 'headroom.exe'
            ProductName = 'Headroom'; ProductVersion = $Version
        } }
    } } }
}
try {
    Copy-Item -LiteralPath $icon -Destination $iconFile
    [IO.File]::WriteAllText($inputFile, ($description | ConvertTo-Json -Depth 10), (New-Object Text.UTF8Encoding($false)))
    & go run github.com/tc-hib/go-winres@v0.3.3 make --in $inputFile --arch $target --out $output --no-suffix
    if ($LASTEXITCODE -ne 0) { throw 'Launcher resource compilation failed.' }
} finally {
    if (Test-Path -LiteralPath $iconFile) { Remove-Item -LiteralPath $iconFile -Force }
    if (Test-Path -LiteralPath $inputFile) { Remove-Item -LiteralPath $inputFile -Force }
}
