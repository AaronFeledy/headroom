param([Parameter(Mandatory)][string]$TestExecutable)
$ErrorActionPreference = 'Stop'
if ($env:GITHUB_ACTIONS -cne 'true' -or $env:RUNNER_ENVIRONMENT -cne 'github-hosted') {
    throw 'Desktop preference changes are restricted to disposable hosted CI.'
}
Add-Type @'
using System;
using System.Runtime.InteropServices;
public static class MotionPreference {
    [DllImport("user32.dll", EntryPoint="SystemParametersInfoW", SetLastError=true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static extern bool Get(uint action, uint parameter, out int value, uint flags);
    [DllImport("user32.dll", EntryPoint="SystemParametersInfoW", SetLastError=true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static extern bool Set(uint action, uint parameter, IntPtr value, uint flags);
}
'@
$previous = 0
if (![MotionPreference]::Get(0x1042, 0, [ref]$previous, 0)) { throw 'Cannot read client area animation preference.' }
try {
    if (![MotionPreference]::Set(0x1043, 0, [IntPtr]::Zero, 3)) { throw 'Cannot disable client area animations.' }
    $current = 1
    if (![MotionPreference]::Get(0x1042, 0, [ref]$current, 0) -or $current -ne 0) { throw 'Reduced motion was not applied.' }
    $env:HEADROOM_EXPECT_PLATFORM_REDUCED_MOTION = '1'
    $env:QT_QPA_PLATFORM = 'windows'
    $env:QT_QUICK_BACKEND = 'software'
    $result = Join-Path $env:RUNNER_TEMP 'headroom-native-motion.txt'
    $process = Start-Process -FilePath $TestExecutable -ArgumentList @('platformReducedMotion', '-o', "$result,txt") -PassThru
    $timedOut = !$process.WaitForExit(90000)
    if ($timedOut) { $process.Kill(); $process.WaitForExit() }
    if (Test-Path $result) { Get-Content -Raw $result }
    if ($timedOut -or $process.ExitCode -ne 0) { throw 'Native reduced-motion test failed or timed out.' }
} finally {
    if (![MotionPreference]::Set(0x1043, 0, [IntPtr]::new($previous), 3)) { throw 'Cannot restore client area animations.' }
}
