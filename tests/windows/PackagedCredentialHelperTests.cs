using System.Diagnostics;
using System.Text.Json;
using Microsoft.Data.Sqlite;

if (!OperatingSystem.IsWindows())
{
    Console.WriteLine("SKIP: Packaged Windows helper execution requires Windows.");
    return 0;
}
if (args.Length != 1 || !File.Exists(args[0])) return 2;
var roaming = Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData);
var local = Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData);
var profiles = Path.Combine(roaming, "Mozilla", "Firefox", "Profiles");
var discoveryRoots = new[] {
    Path.Combine(local, "Google", "Chrome", "User Data"),
    Path.Combine(local, "Microsoft", "Edge", "User Data"),
    Path.Combine(local, "BraveSoftware", "Brave-Browser", "User Data"), profiles
};
if (discoveryRoots.Any(path => Directory.Exists(path) && Directory.EnumerateFileSystemEntries(path).Any()))
    throw new Exception("Refusing to run the packaged helper fixture where an existing browser profile could be discovered.");
var profile = Path.Combine(profiles, "000-headroom-package-fixture-" + Guid.NewGuid().ToString("N"));
var snapshots = Directory.CreateTempSubdirectory("headroom-package-snapshots-").FullName;
try
{
    Directory.CreateDirectory(profile);
    var database = Path.Combine(profile, "cookies.sqlite");
    using (var connection = new SqliteConnection(new SqliteConnectionStringBuilder { DataSource = database }.ToString()))
    {
        connection.Open();
        using var command = connection.CreateCommand();
        command.CommandText = "CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,expiry INTEGER,lastAccessed INTEGER);" +
            "INSERT INTO moz_cookies VALUES('auth.grok.com','sso','synthetic-package-cookie',4102444800,1);";
        command.ExecuteNonQuery();
    }
    var start = new ProcessStartInfo(args[0], "grok")
    {
        UseShellExecute = false,
        RedirectStandardOutput = true,
        RedirectStandardError = true,
        CreateNoWindow = true,
    };
    start.Environment["HEADROOM_CREDENTIAL_SNAPSHOT_ROOT"] = snapshots;
    using var helper = Process.Start(start) ?? throw new Exception("helper did not start");
    var outputTask = helper.StandardOutput.ReadToEndAsync();
    var errorTask = helper.StandardError.ReadToEndAsync();
    using var deadline = new CancellationTokenSource(TimeSpan.FromSeconds(15));
    try { await helper.WaitForExitAsync(deadline.Token); }
    catch (OperationCanceledException) { helper.Kill(true); await helper.WaitForExitAsync(); throw new Exception("helper timed out"); }
    var output = await outputTask;
    var error = await errorTask;
    if (helper.ExitCode != 0 || error.Length != 0) throw new Exception($"helper failed with {helper.ExitCode}");
    using var document = JsonDocument.Parse(output);
    if (document.RootElement.GetProperty("provider").GetString() != "grok" ||
        document.RootElement.GetProperty("cookie").GetString() != "sso=synthetic-package-cookie" ||
        document.RootElement.GetProperty("source").GetString() != "Firefox" ||
        string.Join(',', document.RootElement.EnumerateObject().Select(property => property.Name)) != "provider,cookie,source,checked" ||
        document.RootElement.GetProperty("checked").EnumerateArray().Last().GetProperty("name").GetString() != "Firefox" ||
        document.RootElement.GetProperty("checked").EnumerateArray().Last().GetProperty("status").GetString() != "signed_in")
        throw new Exception("helper did not read the generated SQLite fixture");
    if (Directory.EnumerateFileSystemEntries(snapshots).Any()) throw new Exception("helper left a browser snapshot behind");
    return 0;
}
finally
{
    try { Directory.Delete(profile, true); } catch { }
    try { Directory.Delete(snapshots, true); } catch { }
}
