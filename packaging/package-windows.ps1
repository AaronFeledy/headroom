[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Version,
    [Parameter(Mandatory)][string]$Architecture,
    [Parameter(Mandatory)][string]$BuildDir,
    [Parameter(Mandatory)][string]$WorkDir,
    [Parameter(Mandatory)][string]$OutputDir,
    [Parameter(Mandatory)][string]$Server,
    [Parameter(Mandatory)][string]$CredentialHelper,
    [Parameter(Mandatory)][string]$Launcher,
    [Parameter(Mandatory)][string]$Manager,
    [Parameter(Mandatory)][string]$CLI,
    [Parameter(Mandatory)][string]$CLILauncher,
    [Parameter(Mandatory)][string]$QtRoot,
    [Parameter(Mandatory)][string]$QtSourceCache,
    [Parameter(Mandatory)][string]$ProjectAssets
)
$ErrorActionPreference = 'Stop'
$assetArch = if ($Architecture -eq 'x86_64') { 'x64' } elseif ($Architecture -eq 'arm64') { 'arm64' } else { throw "Unsupported architecture: $Architecture" }
$selectedTarget = [string]$env:VSCMD_ARG_TGT_ARCH
if ($selectedTarget -and $selectedTarget -ine $assetArch) { throw "Selected MSVC target $selectedTarget does not match package target $assetArch." }
& $Manager asset-name --version $Version --platform windows --arch $Architecture | Out-Null
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
$asset = "Headroom-v$Version-windows-$assetArch.zip"
$packageRoot = Join-Path $WorkDir "Headroom-v$Version-windows-$assetArch"
if (Test-Path $packageRoot) { Remove-Item -Recurse -Force $packageRoot }
New-Item -ItemType Directory -Force (Join-Path $packageRoot 'bundle'), (Join-Path $packageRoot 'bootstrap'), $OutputDir | Out-Null
cmake --install $BuildDir --prefix (Join-Path $packageRoot 'bundle')
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
$buildMetadataPath = Join-Path $BuildDir 'headroom-build-metadata.json'
if (!(Test-Path -LiteralPath $buildMetadataPath -PathType Leaf)) { throw 'Desktop build metadata is missing.' }
$buildMetadata = Get-Content -LiteralPath $buildMetadataPath -Raw | ConvertFrom-Json
if ($buildMetadata.product -cne 'Headroom' -or $buildMetadata.version -cne $Version) { throw 'Desktop build version does not match package version.' }
$qtVersion = [string]$buildMetadata.qt_version
if ($qtVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+$') { throw 'Desktop Qt version is invalid.' }
$platformDestination = Join-Path $packageRoot 'bundle/plugins/platforms'
New-Item -ItemType Directory -Force $platformDestination | Out-Null
foreach ($pluginName in @('qwindows.dll', 'qoffscreen.dll')) {
    $plugin = @(Get-ChildItem $QtRoot -Recurse -File -Filter $pluginName | Where-Object { $_.Directory.Name -eq 'platforms' })
    if ($plugin.Count -ne 1) { throw "Expected one Qt platform plugin $pluginName, found $($plugin.Count)" }
    Copy-Item $plugin[0].FullName (Join-Path $platformDestination $pluginName) -Force
}
$redistRoot = [string]$env:VCToolsRedistDir
if (!$redistRoot -or !(Test-Path -LiteralPath $redistRoot -PathType Container)) { throw 'VCToolsRedistDir does not identify the selected Visual Studio redistributable root.' }
$redistArchitectureRoot = Join-Path $redistRoot $assetArch
$crtDirectories = @(Get-ChildItem -LiteralPath $redistArchitectureRoot -Directory -Filter 'Microsoft.VC*.CRT')
if ($crtDirectories.Count -ne 1) { throw "Expected one $assetArch MSVC CRT directory under $redistArchitectureRoot, found $($crtDirectories.Count)." }
$crtFiles = @(Get-ChildItem -LiteralPath $crtDirectories[0].FullName -File -Filter '*.dll')
foreach ($requiredRuntime in @('msvcp140.dll', 'vcruntime140.dll')) {
    if (@($crtFiles | Where-Object Name -CEQ $requiredRuntime).Count -ne 1) { throw "Required $assetArch MSVC runtime file is missing: $requiredRuntime" }
}
Copy-Item -LiteralPath $crtFiles.FullName -Destination (Join-Path $packageRoot 'bundle/bin')
Copy-Item $Server (Join-Path $packageRoot 'bundle/bin/usage-server.exe')
Copy-Item $CredentialHelper (Join-Path $packageRoot 'bundle/bin/headroom-credential-helper.exe')
Copy-Item $Launcher (Join-Path $packageRoot 'bootstrap/headroom.exe')
$launcherBranding = (Get-Item -LiteralPath $Launcher).VersionInfo
if ($launcherBranding.ProductName -cne 'Headroom' -or $launcherBranding.ProductVersion -cne $Version) {
    throw 'Stable launcher branding/version resources are missing or do not match the package.'
}
Copy-Item $Manager (Join-Path $packageRoot 'bootstrap/headroom-package.exe')
Copy-Item $Manager (Join-Path $packageRoot 'bundle/bin/headroom-package.exe')
Copy-Item $CLI (Join-Path $packageRoot 'bundle/bin/headroom-cli.exe')
Copy-Item $CLILauncher (Join-Path $packageRoot 'bootstrap/headroom-cli.exe')
Copy-Item $CLILauncher (Join-Path $packageRoot 'bundle/bin/headroom-cli-launcher.exe')
Copy-Item packaging/THIRD_PARTY_NOTICES.txt (Join-Path $packageRoot 'bundle/share/headroom/THIRD_PARTY_NOTICES.txt')
New-Item -ItemType Directory -Force (Join-Path $packageRoot 'bundle/share/licenses/headroom'), (Join-Path $packageRoot 'bundle/share/licenses/qt'), (Join-Path $packageRoot 'bundle/share/licenses/nuget'), (Join-Path $packageRoot 'bundle/share/licenses/msvc'), (Join-Path $packageRoot 'bundle/share/licenses/go/runtime'), (Join-Path $packageRoot 'bundle/share/licenses/go/protobuf'), (Join-Path $packageRoot 'bundle/share/licenses/go/yaml') | Out-Null
Copy-Item LICENSE (Join-Path $packageRoot 'bundle/share/licenses/headroom/LICENSE')
$trackedQtLicenses = @(Get-ChildItem -LiteralPath 'packaging/licenses/qt' -File)
if ($trackedQtLicenses.Count -lt 5) { throw 'Required Qt license texts are missing.' }
$trackedQtLicenses | Copy-Item -Destination (Join-Path $packageRoot 'bundle/share/licenses/qt')
$qtLicenses = @(@((Join-Path $QtRoot 'LICENSES'), (Join-Path $QtRoot 'licenses')) | Where-Object { Test-Path -LiteralPath $_ -PathType Container } | ForEach-Object { Get-ChildItem -LiteralPath $_ -Recurse -File })
foreach ($license in $qtLicenses) {
    $relative = $license.FullName.Substring($QtRoot.TrimEnd('\','/').Length).TrimStart('\','/')
    $destination = Join-Path $packageRoot "bundle/share/licenses/qt/$relative"
    New-Item -ItemType Directory -Force (Split-Path $destination) | Out-Null
    Copy-Item -LiteralPath $license.FullName -Destination $destination
}
python packaging/qt_attributions.py package --source-cache $QtSourceCache --qt-root $QtRoot --payload-root (Join-Path $packageRoot 'bundle') --output (Join-Path $packageRoot 'bundle/share/licenses/qt/attributions') --modules qtbase qtdeclarative qtshadertools qtsvg
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
$qtAttributions = Get-Content -LiteralPath (Join-Path $packageRoot 'bundle/share/licenses/qt/attributions/index.json') -Raw | ConvertFrom-Json
if ($qtAttributions.qt_version -cne $qtVersion) { throw 'Qt attribution version does not match deployed Qt.' }

if (!(Test-Path -LiteralPath $ProjectAssets -PathType Leaf)) { throw "Credential helper project.assets.json not found: $ProjectAssets" }
$assets = Get-Content -LiteralPath $ProjectAssets -Raw | ConvertFrom-Json
$packageRoots = @($assets.packageFolders.PSObject.Properties.Name)
$packageRoots = @($packageRoots | Where-Object { Test-Path -LiteralPath $_ -PathType Container })
if ($packageRoots.Count -eq 0) { throw 'No resolved NuGet package root is available.' }
$resolvedPackages = @($assets.libraries.PSObject.Properties | Where-Object { $_.Value.type -ceq 'package' } | ForEach-Object { $_.Name })
$framework = @($assets.project.frameworks.PSObject.Properties.Value)
if ($framework.Count -ne 1) { throw 'Expected one credential-helper target framework.' }
$rid = if ($Architecture -eq 'arm64') { 'win-arm64' } else { 'win-x64' }
$runtimePackages = @($framework[0].downloadDependencies | Where-Object { $_.name -in @("Microsoft.NETCore.App.Host.$rid", "Microsoft.NETCore.App.Runtime.$rid") } | ForEach-Object {
    $resolvedVersion = ([string]$_.version).Trim('[',']').Split(',')[0].Trim()
    "$($_.name)/$resolvedVersion"
})
$inventoryPackages = @($resolvedPackages + $runtimePackages | Sort-Object -Unique)
if ($inventoryPackages.Count -lt 9 -or $inventoryPackages -notcontains 'SourceGear.sqlite3/3.50.4.5') { throw 'Credential-helper NuGet dependency inventory is incomplete.' }
foreach ($package in $inventoryPackages) {
    $separator = $package.LastIndexOf('/')
    if ($separator -le 0) { throw "Invalid resolved NuGet package key: $package" }
    $packageName = $package.Substring(0, $separator).ToLowerInvariant()
    $packageVersion = $package.Substring($separator + 1).ToLowerInvariant()
    $sourceRoot = @($packageRoots | ForEach-Object { Join-Path $_ "$packageName/$packageVersion" } | Where-Object { Test-Path -LiteralPath $_ -PathType Container } | Select-Object -First 1)
    if ($sourceRoot.Count -ne 1) { throw "Resolved NuGet package is missing: $package" }
    $sourceRoot = $sourceRoot[0]
    $destinationRoot = Join-Path $packageRoot "bundle/share/licenses/nuget/$packageName/$packageVersion"
    New-Item -ItemType Directory -Force $destinationRoot | Out-Null
    $nuspecs = @(Get-ChildItem -LiteralPath $sourceRoot -File -Filter '*.nuspec')
    if ($nuspecs.Count -ne 1) { throw "Expected one nuspec for $package" }
    Copy-Item -LiteralPath $nuspecs[0].FullName -Destination (Join-Path $destinationRoot $nuspecs[0].Name)
    $licenseFiles = @(Get-ChildItem -LiteralPath $sourceRoot -Recurse -File | Where-Object { $_.Name -like 'LICENSE*' -or $_.Name -like '*NOTICE*' })
    foreach ($license in $licenseFiles) {
        $relative = $license.FullName.Substring($sourceRoot.TrimEnd('\','/').Length).TrimStart('\','/')
        $destination = Join-Path $destinationRoot $relative
        New-Item -ItemType Directory -Force (Split-Path $destination) | Out-Null
        Copy-Item -LiteralPath $license.FullName -Destination $destination
    }
    if ($licenseFiles.Count -eq 0) {
        [xml]$nuspec = Get-Content -LiteralPath $nuspecs[0].FullName -Raw
        $expression = [string]$nuspec.package.metadata.license.InnerText
        $expressionSource = if ($expression -ceq 'Apache-2.0') { 'packaging/licenses/Apache-2.0.txt' } elseif ($expression -ceq 'MIT') { 'packaging/licenses/DotNet-MIT.txt' } else { throw "No license text is available for $package ($expression)." }
        Copy-Item -LiteralPath $expressionSource -Destination (Join-Path $destinationRoot "LICENSE-$expression.txt")
    }
}
Copy-Item (Join-Path (go env GOROOT) 'LICENSE') (Join-Path $packageRoot 'bundle/share/licenses/go/runtime/LICENSE')
$moduleCache = go env GOMODCACHE
Copy-Item (Join-Path $moduleCache 'google.golang.org/protobuf@v1.36.11/LICENSE') (Join-Path $packageRoot 'bundle/share/licenses/go/protobuf/LICENSE')
Copy-Item (Join-Path $moduleCache 'gopkg.in/yaml.v3@v3.0.1/LICENSE') (Join-Path $packageRoot 'bundle/share/licenses/go/yaml/LICENSE')
Copy-Item (Join-Path $moduleCache 'gopkg.in/yaml.v3@v3.0.1/NOTICE') (Join-Path $packageRoot 'bundle/share/licenses/go/yaml/NOTICE')
foreach ($module in @('sys', 'term')) {
    Push-Location packaging/headroom-manager
    try { $moduleDir = go list -m -f '{{.Dir}}' "golang.org/x/$module"; if ($LASTEXITCODE -ne 0) { throw 'Go module lookup failed.' } }
    finally { Pop-Location }
    $destination = Join-Path $packageRoot "bundle/share/licenses/go/x-$module"
    New-Item -ItemType Directory -Force $destination | Out-Null
    Copy-Item (Join-Path $moduleDir 'LICENSE') (Join-Path $destination 'LICENSE')
}
'Microsoft Visual C++ runtime files are redistributed under the Visual Studio license.' | Set-Content -Encoding UTF8 (Join-Path $packageRoot 'bundle/share/licenses/msvc/NOTICE.txt')
& $Manager create-package --root $packageRoot --output (Join-Path $OutputDir $asset) --version $Version --platform windows --arch $Architecture --qt-version $qtVersion --baseline $(if ($Architecture -eq 'arm64') { 'windows-11-arm64-qt-msvc2022' } else { 'windows-10-1809-x64-qt-msvc2022' })
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& $Manager verify --archive (Join-Path $OutputDir $asset) --version $Version --platform windows --arch $Architecture --asset $asset
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
