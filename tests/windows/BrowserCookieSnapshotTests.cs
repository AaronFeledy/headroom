using Microsoft.Data.Sqlite;
using ClaudeUsageWidget.Services;
using Headroom.CredentialHelper;
using System.Text;
using System.Security.Cryptography;
using System.Text.Json;

internal sealed class BrowserCookieSnapshotTests
{
    public Task Test_SnapshotCopiesWalDatabaseAndCleansArtifacts()
    {
        using var fixture = CookieDbFixture.Create();
        fixture.InsertCookieWithoutCheckpoint("WorkosCursorSessionToken", "encrypted-value");

        string snapshotRoot;
        using (var snapshot = BrowserCookieDatabaseSnapshot.Create(fixture.DatabasePath))
        {
            snapshotRoot = snapshot.DirectoryPath;
            AssertEqual(true, File.Exists(snapshot.DatabasePath));
            using var connection = new SqliteConnection($"Data Source={snapshot.DatabasePath};Mode=ReadOnly;Pooling=False");
            connection.Open();
            using var command = connection.CreateCommand();
            command.CommandText = "SELECT value FROM cookies WHERE name = 'WorkosCursorSessionToken'";
            AssertEqual("encrypted-value", (string?)command.ExecuteScalar());
        }

        AssertEqual(false, Directory.Exists(snapshotRoot));
        return Task.CompletedTask;
    }

    public Task Test_SnapshotCleansArtifactsWhenOpenFails()
    {
        using var fixture = CookieDbFixture.Create();
        fixture.InsertCookieWithoutCheckpoint("WorkosCursorSessionToken", "encrypted-value");
        var rootsBefore = SnapshotRoots();

        try
        {
            using var snapshot = BrowserCookieDatabaseSnapshot.Create(Path.Combine(fixture.TempPath, "missing.sqlite"));
        }
        catch (FileNotFoundException)
        {
        }

        AssertEqual(rootsBefore, SnapshotRoots());
        return Task.CompletedTask;
    }

    public Task Test_ReaderUsesDomainBoundaryExpiryAndProfilePriority()
    {
        using var fixture = BrowserFixture.Create();
        fixture.AddChromiumProfile("Profile 1", new ChromiumCookie("auth.cursor.com", "WorkosCursorSessionToken", fixture.EncryptAes("later-profile"), fixture.FutureChromium));
        fixture.AddChromiumProfile("Default",
            new ChromiumCookie("evilcursor.com", "WorkosCursorSessionToken", fixture.EncryptAes("lookalike"), fixture.FutureChromium),
            new ChromiumCookie("cursor.com", "WorkosCursorSessionToken", fixture.EncryptAes("expired"), fixture.PastChromium),
            new ChromiumCookie("api.cursor.sh", "WorkosCursorSessionToken", fixture.EncryptAes("default-profile"), fixture.FutureChromium));

        var reader = fixture.Reader();
        AssertEqual("WorkosCursorSessionToken=default-profile", reader.ReadCursorCookieHeader());
        fixture.AssertNoSnapshots();
        return Task.CompletedTask;
    }

    public Task Test_ReaderUsesFirefoxWalSnapshotAndDeterministicProfiles()
    {
        using var fixture = BrowserFixture.Create();
        fixture.AddFirefoxProfile("z-profile", new FirefoxCookie(".grok.com", "sso", "later", fixture.FutureUnix));
        fixture.AddFirefoxProfile("a-profile",
            new FirefoxCookie("notgrok.com", "sso", "lookalike", fixture.FutureUnix),
            new FirefoxCookie("auth.grok.com", "sso", "first", fixture.FutureUnix));
        AssertEqual("sso=first", fixture.Reader().ReadGrokCookieHeader());
        fixture.AssertNoSnapshots();
        return Task.CompletedTask;
    }

    public Task Test_ReaderSkipsUnsupportedAndMalformedEncryption()
    {
        using var fixture = BrowserFixture.Create();
        var malformed = Encoding.ASCII.GetBytes("v10short");
        var unsupported = Encoding.ASCII.GetBytes("v20new-browser-protection");
        fixture.AddChromiumProfile("Default",
            new ChromiumCookie("cursor.com", "__Secure-next-auth.session-token", malformed, fixture.FutureChromium),
            new ChromiumCookie("cursor.com", "next-auth.session-token", unsupported, fixture.FutureChromium),
            new ChromiumCookie("cursor.com", "WorkosCursorSessionToken", Encoding.UTF8.GetBytes("legacy-value"), fixture.FutureChromium));
        AssertEqual("WorkosCursorSessionToken=legacy-value", fixture.Reader().ReadCursorCookieHeader());
        fixture.AssertNoSnapshots();
        return Task.CompletedTask;
    }

    public Task Test_ReaderContinuesAfterMalformedProfile()
    {
        using var fixture = BrowserFixture.Create();
        var broken = Directory.CreateDirectory(Path.Combine(fixture.ChromiumRoot, "Default", "Network")).FullName;
        File.WriteAllText(Path.Combine(broken, "Cookies"), "not a sqlite database");
        fixture.AddChromiumProfile("Profile 1", new ChromiumCookie("cursor.com", "WorkosCursorSessionToken", fixture.EncryptAes("second-profile"), fixture.FutureChromium));
        AssertEqual("WorkosCursorSessionToken=second-profile", fixture.Reader().ReadCursorCookieHeader());
        fixture.AssertNoSnapshots();
        return Task.CompletedTask;
    }

    public Task Test_ReaderDiscoversFirefoxProfilesLazily()
    {
        using var fixture = BrowserFixture.Create();
        var reader = fixture.Reader();
        fixture.AddFirefoxProfile("created-later", new FirefoxCookie("grok.com", "sso", "late-cookie", fixture.FutureUnix));
        AssertEqual("sso=late-cookie", reader.ReadGrokCookieHeader());
        fixture.AssertNoSnapshots();
        return Task.CompletedTask;
    }

    public Task Test_HelperProtocolAcceptsOnlyKnownProviderAndBoundsOutput()
    {
        var output = new StringWriter();
        AssertEqual(2, CredentialHelperProtocol.Run(["unknown"], new ProtocolCookieReader("x"), output));
        AssertEqual(string.Empty, output.ToString());
        output = new StringWriter();
        AssertEqual(0, CredentialHelperProtocol.Run(["cursor"], new ProtocolCookieReader("fixture-cookie"), output));
        AssertEqual(true, output.ToString().Contains("fixture-cookie", StringComparison.Ordinal));
        output = new StringWriter();
        AssertEqual(3, CredentialHelperProtocol.Run(["grok"], new ProtocolCookieReader(new string('x', CredentialHelperProtocol.MaximumOutputBytes)), output));
        AssertEqual(string.Empty, output.ToString());
        return Task.CompletedTask;
    }

    public Task Test_HelperJsonShapeSourceAndNulls()
    {
        using var fixture = BrowserFixture.Create();
        fixture.AddChromiumProfile("Default", new ChromiumCookie("cursor.com", "WorkosCursorSessionToken", "v20blocked"u8.ToArray(), fixture.FutureChromium));
        fixture.AddFirefoxProfile("default", new FirefoxCookie("cursor.com", "WorkosCursorSessionToken", "synthetic-firefox", fixture.FutureUnix));
        var output = new StringWriter();
        AssertEqual(0, CredentialHelperProtocol.Run(["cursor"], fixture.Reader(), output));
        AssertEqual("{\"provider\":\"cursor\",\"cookie\":\"WorkosCursorSessionToken=synthetic-firefox\",\"source\":\"Firefox\",\"checked\":[{\"name\":\"Chrome\",\"status\":\"encrypted\"},{\"name\":\"Firefox\",\"status\":\"signed_in\"}]}", output.ToString());
        output = new StringWriter();
        AssertEqual(0, CredentialHelperProtocol.Run(["grok"], fixture.Reader(), output));
        AssertEqual("{\"provider\":\"grok\",\"cookie\":null,\"source\":null,\"checked\":[{\"name\":\"Chrome\",\"status\":\"signed_out\"},{\"name\":\"Firefox\",\"status\":\"signed_out\"}]}", output.ToString());
        using var json = JsonDocument.Parse(output.ToString());
        AssertEqual("provider,cookie,source,checked", string.Join(',', json.RootElement.EnumerateObject().Select(property => property.Name)));
        fixture.AssertNoSnapshots();
        return Task.CompletedTask;
    }

    public Task Test_InstalledBrowsersOrderAndSourcePriority()
    {
        using var fixture = BrowserFixture.Create();
        var edge = Directory.CreateDirectory(Path.Combine(fixture.Root, "edge")).FullName;
        var brave = Directory.CreateDirectory(Path.Combine(fixture.Root, "brave")).FullName;
        fixture.AddChromiumProfile("Default", new ChromiumCookie("grok.com", "sso", Encoding.UTF8.GetBytes("chrome"), fixture.FutureChromium));
        fixture.AddChromiumProfileAt(edge, "Default", new ChromiumCookie("grok.com", "sso", Encoding.UTF8.GetBytes("edge"), fixture.FutureChromium));
        fixture.AddChromiumProfileAt(brave, "Default", new ChromiumCookie("grok.com", "sso", Encoding.UTF8.GetBytes("brave"), fixture.FutureChromium));
        fixture.AddFirefoxProfile("default", new FirefoxCookie("grok.com", "sso", "firefox", fixture.FutureUnix));
        var roots = new BrowserCookieRoot[] { new("Brave", brave), new("Edge", edge), new("Chrome", fixture.ChromiumRoot), new("Absent", Path.Combine(fixture.Root, "absent")) };
        var result = fixture.Reader(roots).ReadGrokCookies();
        AssertEqual("Chrome,Edge,Brave,Firefox", string.Join(',', result.Checked.Select(check => check.Name)));
        AssertEqual(true, result.Checked.All(check => check.Status == "signed_in"));
        AssertEqual("Chrome", result.Source);
        AssertEqual("sso=chrome", result.Cookie);
        AssertEqual("Edge", fixture.Reader([new("Edge", edge), new("Brave", brave)]).ReadGrokCookies().Source);
        AssertEqual("Brave", fixture.Reader([new("Brave", brave)]).ReadGrokCookies().Source);
        fixture.AssertNoSnapshots();
        return Task.CompletedTask;
    }

    public Task Test_ChromiumStatusesAndProfileAggregation()
    {
        foreach (var scenario in new[] { "signed_out", "expired", "encrypted", "aes_failure", "dpapi_failure", "unreadable" })
        {
            using var fixture = BrowserFixture.Create(bytes => bytes.Length == 32 ? bytes : throw new CryptographicException());
            if (scenario == "unreadable")
            {
                var network = Directory.CreateDirectory(Path.Combine(fixture.ChromiumRoot, "Default", "Network")).FullName;
                File.WriteAllText(Path.Combine(network, "Cookies"), "invalid sqlite");
            }
            else
            {
                var value = scenario == "aes_failure" ? fixture.EncryptAes("synthetic") : Encoding.UTF8.GetBytes(scenario == "encrypted" ? "v20blocked" : "legacy");
                if (scenario == "aes_failure") value[^1] ^= 1;
                fixture.AddChromiumProfile("Default", new ChromiumCookie(scenario == "signed_out" ? "unrelated.com" : "cursor.com", "WorkosCursorSessionToken", value,
                    scenario == "expired" ? fixture.PastChromium : fixture.FutureChromium));
            }
            var expected = scenario is "aes_failure" or "dpapi_failure" ? "encrypted" : scenario;
            var result = fixture.Reader().ReadCursorCookies();
            AssertEqual(expected, result.Checked.Single().Status);
            AssertEqual<string?>(null, result.Cookie);
            AssertEqual<string?>(null, result.Source);
            fixture.AddChromiumProfile("Profile 1", new ChromiumCookie("cursor.com", "WorkosCursorSessionToken", fixture.EncryptAes("usable"), fixture.FutureChromium));
            result = fixture.Reader().ReadCursorCookies();
            AssertEqual("signed_in", result.Checked.Single().Status);
            AssertEqual("Chrome", result.Source);
            AssertEqual("WorkosCursorSessionToken=usable", fixture.Reader().ReadCursorCookieHeader());
            fixture.AssertNoSnapshots();
        }
        return Task.CompletedTask;
    }

    public Task Test_LockedAndStatusPrecedence()
    {
        foreach (var failure in new Exception[] { new UnauthorizedAccessException(), new IOException("sharing", unchecked((int)0x80070020)),
            new IOException("locking", unchecked((int)0x80070021)), new IOException("other"), new SqliteException("busy", 5), new SqliteException("locked", 6) })
        {
            using var fixture = BrowserFixture.Create();
            fixture.AddChromiumProfile("Default");
            var expected = failure is IOException && (failure.HResult & 0xffff) is not (32 or 33) ? "unreadable" : "locked";
            var reader = fixture.Reader(snapshotFactory: path => Path.GetDirectoryName(Path.GetDirectoryName(path)) == Path.Combine(fixture.ChromiumRoot, "Default")
                ? throw failure : BrowserCookieDatabaseSnapshot.Create(path, fixture.SnapshotRoot));
            AssertEqual(expected, reader.ReadCursorCookies().Checked.Single().Status);
            fixture.AddChromiumProfile("Profile 1", new ChromiumCookie("cursor.com", "WorkosCursorSessionToken", "v20blocked"u8.ToArray(), fixture.FutureChromium));
            AssertEqual("encrypted", reader.ReadCursorCookies().Checked.Single().Status);
            fixture.AssertNoSnapshots();
        }
        using (var fixture = BrowserFixture.Create())
        {
            fixture.AddChromiumProfile("Default");
            fixture.AddChromiumProfile("Profile 1", new ChromiumCookie("cursor.com", "WorkosCursorSessionToken", "v20expired"u8.ToArray(), fixture.PastChromium));
            var unreadable = fixture.Reader(snapshotFactory: path => path.Contains("Default", StringComparison.Ordinal)
                ? throw new InvalidOperationException() : BrowserCookieDatabaseSnapshot.Create(path, fixture.SnapshotRoot));
            AssertEqual("expired", unreadable.ReadCursorCookies().Checked.Single().Status);
            var locked = fixture.Reader(snapshotFactory: path => path.Contains("Default", StringComparison.Ordinal)
                ? throw new UnauthorizedAccessException() : BrowserCookieDatabaseSnapshot.Create(path, fixture.SnapshotRoot));
            AssertEqual("locked", locked.ReadCursorCookies().Checked.Single().Status);
        }
        return Task.CompletedTask;
    }

    public Task Test_FirefoxDefaultsAbsolutePathsAndStatuses()
    {
        using var fixture = BrowserFixture.Create();
        fixture.AddFirefoxProfile("a-first", new FirefoxCookie("cursor.com", "WorkosCursorSessionToken", "alphabetical", fixture.FutureUnix));
        fixture.AddFirefoxProfile("z-default", new FirefoxCookie("cursor.com", "WorkosCursorSessionToken", "preferred", fixture.FutureUnix));
        fixture.AddFirefoxProfile("m-expired", new FirefoxCookie("grok.com", "sso", "expired", fixture.FutureUnix - 7200));
        var absolute = Path.Combine(fixture.Root, "external-default");
        fixture.AddFirefoxProfile(absolute, new FirefoxCookie("cursor.com", "WorkosCursorSessionToken", "absolute", fixture.FutureUnix));
        var root = Path.GetDirectoryName(fixture.FirefoxRoot)!;
        File.WriteAllText(Path.Combine(root, "installs.ini"), "[InstallOne]\nDefault=Profiles/z-default\n[broken\ninvalid\n");
        File.WriteAllText(Path.Combine(root, "profiles.ini"), $"[Profile0]\nPath={absolute}\nIsRelative=0\nDefault=1\n");
        AssertEqual("z-default,external-default,a-first,m-expired", string.Join(',', WindowsBrowserCookieReaderOptions.DiscoverFirefoxProfiles(root).Select(Path.GetFileName)));
        AssertEqual("WorkosCursorSessionToken=preferred", fixture.Reader().ReadCursorCookieHeader());
        AssertEqual("expired", fixture.Reader().ReadGrokCookies().Checked.Single(check => check.Name == "Firefox").Status);
        File.Delete(Path.Combine(root, "installs.ini"));
        AssertEqual("WorkosCursorSessionToken=absolute", fixture.Reader().ReadCursorCookieHeader());
        File.WriteAllText(Path.Combine(root, "profiles.ini"), "malformed\n[Profile0]\nPath=missing\nDefault=1\n");
        AssertEqual("a-first,m-expired,z-default", string.Join(',', WindowsBrowserCookieReaderOptions.DiscoverFirefoxProfiles(root).Select(Path.GetFileName)));
        var locked = fixture.Reader(snapshotFactory: _ => throw new UnauthorizedAccessException());
        AssertEqual("locked", locked.ReadCursorCookies().Checked.Single(check => check.Name == "Firefox").Status);
        fixture.AssertNoSnapshots();
        return Task.CompletedTask;
    }

    public Task Test_ReaderDecryptsNativeDpapiAndAesFixturesOnWindows()
    {
#if WINDOWS
        var unprotect = (byte[] bytes) => ProtectedData.Unprotect(bytes, null, DataProtectionScope.CurrentUser);
        var protect = (byte[] bytes) => ProtectedData.Protect(bytes, null, DataProtectionScope.CurrentUser);
        using (var aesFixture = BrowserFixture.Create(unprotect, protect))
        {
            aesFixture.AddChromiumProfile("Default", new ChromiumCookie("cursor.com", "WorkosCursorSessionToken",
                aesFixture.EncryptAes("native-aes"), aesFixture.FutureChromium));
            AssertEqual("WorkosCursorSessionToken=native-aes", aesFixture.Reader().ReadCursorCookieHeader());
        }
        using (var dpapiFixture = BrowserFixture.Create(unprotect, protect))
        {
            var protectedCookie = protect(Encoding.UTF8.GetBytes("native-dpapi"));
            dpapiFixture.AddChromiumProfile("Default", new ChromiumCookie("cursor.com", "WorkosCursorSessionToken", protectedCookie, dpapiFixture.FutureChromium));
            AssertEqual("WorkosCursorSessionToken=native-dpapi", dpapiFixture.Reader().ReadCursorCookieHeader());
        }
#endif
        return Task.CompletedTask;
    }

    private static int SnapshotRoots() => Directory.GetDirectories(Path.GetTempPath(), "claude-usage-cookies-*").Length;

    private static void AssertEqual<T>(T expected, T actual)
    {
        if (!EqualityComparer<T>.Default.Equals(expected, actual))
        {
            throw new InvalidOperationException($"Expected {expected}, got {actual}");
        }
    }
}

internal sealed class ProtocolCookieReader(string value) : IBrowserCookieReader
{
    public string? ReadCursorCookieHeader() => value;
    public string? ReadGrokCookieHeader() => value;
    public BrowserCookieReadResult ReadCursorCookies() => new(value, "Chrome", [new("Chrome", "signed_in")]);
    public BrowserCookieReadResult ReadGrokCookies() => ReadCursorCookies();
}

internal sealed record ChromiumCookie(string Host, string Name, byte[] Value, long Expiry);
internal sealed record FirefoxCookie(string Host, string Name, string Value, long Expiry);

internal sealed class BrowserFixture : IDisposable
{
    private static readonly DateTimeOffset Now = new(2026, 9, 9, 12, 0, 0, TimeSpan.Zero);
    private readonly List<SqliteConnection> _writers = [];
    private readonly Func<byte[], byte[]> _unprotect;
    private readonly byte[] _key = Enumerable.Range(1, 32).Select(i => (byte)i).ToArray();
    public string Root { get; }
    public string ChromiumRoot { get; }
    public string FirefoxRoot { get; }
    public string SnapshotRoot { get; }
    public long FutureUnix => Now.AddHours(1).ToUnixTimeSeconds();
    public long FutureChromium => ChromiumTimestamp(Now.AddHours(1));
    public long PastChromium => ChromiumTimestamp(Now.AddHours(-1));

    private BrowserFixture(string root, Func<byte[], byte[]> unprotect, Func<byte[], byte[]> protectMasterKey)
    {
        Root = root;
        ChromiumRoot = Directory.CreateDirectory(Path.Combine(root, "chromium")).FullName;
        FirefoxRoot = Directory.CreateDirectory(Path.Combine(root, "firefox", "Profiles")).FullName;
        SnapshotRoot = Directory.CreateDirectory(Path.Combine(root, "snapshots")).FullName;
        _unprotect = unprotect;
        var encoded = Convert.ToBase64String("DPAPI"u8.ToArray().Concat(protectMasterKey(_key)).ToArray());
        File.WriteAllText(Path.Combine(ChromiumRoot, "Local State"), $"{{\"os_crypt\":{{\"encrypted_key\":\"{encoded}\"}}}}");
    }

    public static BrowserFixture Create(Func<byte[], byte[]>? unprotect = null, Func<byte[], byte[]>? protectMasterKey = null) =>
        new(Directory.CreateTempSubdirectory("headroom-browser-fixture-").FullName,
            unprotect ?? (bytes => bytes), protectMasterKey ?? (bytes => bytes));

    public WindowsBrowserCookieReader Reader(IReadOnlyList<BrowserCookieRoot>? roots = null, Func<string, BrowserCookieDatabaseSnapshot>? snapshotFactory = null)
    {
        return new WindowsBrowserCookieReader(new WindowsBrowserCookieReaderOptions
        {
            ChromiumRoots = roots ?? [new BrowserCookieRoot("Chrome", ChromiumRoot)],
            FirefoxProfiles = () => WindowsBrowserCookieReaderOptions.DiscoverFirefoxProfiles(Path.GetDirectoryName(FirefoxRoot)!),
            UtcNow = () => Now,
            Unprotect = _unprotect,
            TemporaryRoot = SnapshotRoot,
            SnapshotFactory = snapshotFactory
        });
    }

    public void AssertNoSnapshots()
    {
        var count = Directory.GetDirectories(SnapshotRoot).Length;
        if (count != 0) throw new InvalidOperationException($"Expected no browser snapshot artifacts, got {count}");
    }

    public byte[] EncryptAes(string value)
    {
        var nonce = Enumerable.Range(20, 12).Select(i => (byte)i).ToArray();
        var plain = Encoding.UTF8.GetBytes(value);
        var cipher = new byte[plain.Length];
        var tag = new byte[16];
        using var aes = new AesGcm(_key, 16);
        aes.Encrypt(nonce, plain, cipher, tag);
        return "v10"u8.ToArray().Concat(nonce).Concat(cipher).Concat(tag).ToArray();
    }

    public void AddChromiumProfile(string profileName, params ChromiumCookie[] cookies)
        => AddChromiumProfileAt(ChromiumRoot, profileName, cookies);

    public void AddChromiumProfileAt(string root, string profileName, params ChromiumCookie[] cookies)
    {
        var network = Directory.CreateDirectory(Path.Combine(root, profileName, "Network")).FullName;
        var connection = OpenWal(Path.Combine(network, "Cookies"));
        using (var create = connection.CreateCommand())
        {
            create.CommandText = "CREATE TABLE cookies (host_key TEXT, name TEXT, encrypted_value BLOB, expires_utc INTEGER)";
            create.ExecuteNonQuery();
        }
        foreach (var cookie in cookies)
        {
            using var insert = connection.CreateCommand();
            insert.CommandText = "INSERT INTO cookies(host_key,name,encrypted_value,expires_utc) VALUES($host,$name,$value,$expiry)";
            insert.Parameters.AddWithValue("$host", cookie.Host);
            insert.Parameters.AddWithValue("$name", cookie.Name);
            insert.Parameters.AddWithValue("$value", cookie.Value);
            insert.Parameters.AddWithValue("$expiry", cookie.Expiry);
            insert.ExecuteNonQuery();
        }
    }

    public void AddFirefoxProfile(string profileName, params FirefoxCookie[] cookies)
    {
        var profile = Directory.CreateDirectory(Path.Combine(FirefoxRoot, profileName)).FullName;
        var connection = OpenWal(Path.Combine(profile, "cookies.sqlite"));
        using (var create = connection.CreateCommand())
        {
            create.CommandText = "CREATE TABLE moz_cookies (host TEXT, name TEXT, value TEXT, expiry INTEGER, lastAccessed INTEGER)";
            create.ExecuteNonQuery();
        }
        foreach (var cookie in cookies)
        {
            using var insert = connection.CreateCommand();
            insert.CommandText = "INSERT INTO moz_cookies(host,name,value,expiry,lastAccessed) VALUES($host,$name,$value,$expiry,1)";
            insert.Parameters.AddWithValue("$host", cookie.Host);
            insert.Parameters.AddWithValue("$name", cookie.Name);
            insert.Parameters.AddWithValue("$value", cookie.Value);
            insert.Parameters.AddWithValue("$expiry", cookie.Expiry);
            insert.ExecuteNonQuery();
        }
    }

    private SqliteConnection OpenWal(string path)
    {
        var connection = new SqliteConnection($"Data Source={path};Pooling=False");
        connection.Open();
        using var pragma = connection.CreateCommand();
        pragma.CommandText = "PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0";
        pragma.ExecuteNonQuery();
        _writers.Add(connection);
        return connection;
    }

    private static long ChromiumTimestamp(DateTimeOffset time) => checked((time.ToUnixTimeSeconds() + 11_644_473_600L) * 1_000_000L);

    public void Dispose()
    {
        foreach (var writer in _writers) writer.Dispose();
        Directory.Delete(Root, recursive: true);
    }
}

internal sealed class CookieDbFixture : IDisposable
{
    private readonly SqliteConnection _writer;
    public string TempPath { get; }
    public string DatabasePath { get; }

    private CookieDbFixture(string tempPath, string databasePath, SqliteConnection writer)
    {
        TempPath = tempPath;
        DatabasePath = databasePath;
        _writer = writer;
    }

    public static CookieDbFixture Create()
    {
        var tempPath = Directory.CreateTempSubdirectory().FullName;
        var databasePath = Path.Combine(tempPath, "Cookies");
        var writer = new SqliteConnection($"Data Source={databasePath};Pooling=False");
        writer.Open();
        using var pragma = writer.CreateCommand();
        pragma.CommandText = "PRAGMA journal_mode=WAL";
        pragma.ExecuteNonQuery();
        using var create = writer.CreateCommand();
        create.CommandText = "CREATE TABLE cookies (host_key TEXT, name TEXT, value TEXT, encrypted_value BLOB, expires_utc INTEGER)";
        create.ExecuteNonQuery();
        return new CookieDbFixture(tempPath, databasePath, writer);
    }

    public void InsertCookieWithoutCheckpoint(string name, string value)
    {
        using var transaction = _writer.BeginTransaction();
        using var insert = _writer.CreateCommand();
        insert.Transaction = transaction;
        insert.CommandText = "INSERT INTO cookies(host_key, name, value, encrypted_value, expires_utc) VALUES('.cursor.com', $name, $value, X'', 1)";
        insert.Parameters.AddWithValue("$name", name);
        insert.Parameters.AddWithValue("$value", value);
        insert.ExecuteNonQuery();
        transaction.Commit();
    }

    public void Dispose()
    {
        _writer.Dispose();
        Directory.Delete(TempPath, recursive: true);
    }
}
