[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$InstallRoot,
    [Parameter(Mandatory)][string]$EntryPath
)
$ErrorActionPreference = 'Stop'
$shortcutPath = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Headroom.lnk'
if (!(Test-Path -LiteralPath $shortcutPath -PathType Leaf)) { throw 'Headroom Start menu shortcut is missing.' }
$shell = New-Object -ComObject WScript.Shell
try {
    $shortcut = $shell.CreateShortcut($shortcutPath)
    if ($shortcut.TargetPath -ine [IO.Path]::GetFullPath($EntryPath)) { throw 'Shortcut does not target the stable launcher.' }
    if ($shortcut.IconLocation -ine "$([IO.Path]::GetFullPath($EntryPath)),0") { throw 'Shortcut does not use the stable launcher icon.' }
    if ($shortcut.Arguments) { throw 'Shortcut retained stale launch arguments.' }
    if ($shortcut.WorkingDirectory -ine [IO.Path]::GetFullPath($InstallRoot)) { throw 'Shortcut working directory is wrong.' }
} finally {
    if ($shortcut) { [void][Runtime.InteropServices.Marshal]::ReleaseComObject($shortcut) }
    [void][Runtime.InteropServices.Marshal]::ReleaseComObject($shell)
}
$state = Get-Content -LiteralPath (Join-Path $InstallRoot 'install-state.json') -Raw | ConvertFrom-Json
$desktop = Join-Path $InstallRoot (Join-Path $state.version_path 'bin\headroom.exe')
$version = (Get-Item -LiteralPath $EntryPath).VersionInfo
if ($version.ProductName -cne 'Headroom' -or $version.ProductVersion -cne $state.active_version) { throw 'Stable launcher branding/version resources are missing or stale.' }
if (-not ('HeadroomShortcutTest.Native' -as [type])) {
    Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
namespace HeadroomShortcutTest {
    public static class Native {
        [DllImport("shell32.dll", CharSet=CharSet.Unicode)]
        public static extern uint ExtractIconEx(string path, int index, IntPtr large, IntPtr small, uint count);
    }
}
"@
}
if ([HeadroomShortcutTest.Native]::ExtractIconEx($EntryPath, -1, [IntPtr]::Zero, [IntPtr]::Zero, 0) -eq 0) { throw 'Stable launcher has no embedded icon.' }
Add-Type -AssemblyName System.Drawing
function Get-IconPixels([string]$Path) {
    $icon = [Drawing.Icon]::ExtractAssociatedIcon($Path)
    if (!$icon) { throw "Cannot extract icon from $Path" }
    $bitmap = $null
    $stream = New-Object IO.MemoryStream
    try {
        $bitmap = $icon.ToBitmap()
        foreach ($corner in @(@(0,0), @(($bitmap.Width-1),0), @(0,($bitmap.Height-1)), @(($bitmap.Width-1),($bitmap.Height-1)))) {
            if ($bitmap.GetPixel($corner[0],$corner[1]).A -ne 0) { throw "Icon corner is not transparent: $Path" }
        }
        if ($bitmap.GetPixel([int]($bitmap.Width/2),[int]($bitmap.Height/2)).A -ne 255) { throw "Icon center is missing: $Path" }
        $bitmap.Save($stream, [Drawing.Imaging.ImageFormat]::Png)
        return [Convert]::ToBase64String($stream.ToArray())
    } finally {
        $stream.Dispose()
        if ($bitmap) { $bitmap.Dispose() }
        $icon.Dispose()
    }
}
if ((Get-IconPixels $EntryPath) -cne (Get-IconPixels $desktop)) { throw 'Stable launcher icon differs from the desktop icon.' }
Write-Host 'Start menu target, icon, arguments, and launcher branding verified.'
