if (!OperatingSystem.IsWindows())
{
    Console.WriteLine("SKIP: Native DPAPI fixtures require Windows.");
    return;
}
var tests = new BrowserCookieSnapshotTests();
await tests.Test_ReaderDecryptsNativeDpapiAndAesFixturesOnWindows();
Console.WriteLine("Native DPAPI and AES browser fixtures passed");
