using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using Microsoft.Data.Sqlite;

namespace ClaudeUsageWidget.Services;

public sealed record BrowserCookieRoot(string Name, string UserDataPath);
public sealed record BrowserCookieCheck(string Name, string Status);
public sealed record BrowserCookieReadResult(string? Cookie, string? Source, IReadOnlyList<BrowserCookieCheck> Checked);

public interface IBrowserCookieReader : IProviderCookieReader
{
    BrowserCookieReadResult ReadCursorCookies();
    BrowserCookieReadResult ReadGrokCookies();
}

public sealed class WindowsBrowserCookieReaderOptions
{
    public required IReadOnlyList<BrowserCookieRoot> ChromiumRoots { get; init; }
    public required Func<IReadOnlyList<string>> FirefoxProfiles { get; init; }
    public required Func<DateTimeOffset> UtcNow { get; init; }
    public required Func<byte[], byte[]> Unprotect { get; init; }
    public string? TemporaryRoot { get; init; }
    public Func<bool>? FirefoxInstalled { get; init; }
    public Func<string, BrowserCookieDatabaseSnapshot>? SnapshotFactory { get; init; }

    public static WindowsBrowserCookieReaderOptions ForCurrentUser(string? temporaryRoot = null)
    {
        var local = Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData);
        var roaming = Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData);
        var firefoxRoot = Path.Combine(roaming, "Mozilla", "Firefox");
        return new WindowsBrowserCookieReaderOptions
        {
            ChromiumRoots =
            [
                new("Chrome", Path.Combine(local, "Google", "Chrome", "User Data")),
                new("Edge", Path.Combine(local, "Microsoft", "Edge", "User Data")),
                new("Brave", Path.Combine(local, "BraveSoftware", "Brave-Browser", "User Data"))
            ],
            FirefoxProfiles = () => DiscoverFirefoxProfiles(firefoxRoot),
            FirefoxInstalled = () => Directory.Exists(firefoxRoot),
            UtcNow = () => DateTimeOffset.UtcNow,
            Unprotect = DefaultUnprotect,
            TemporaryRoot = temporaryRoot
        };
    }

    public static IReadOnlyList<string> DiscoverFirefoxProfiles(string root)
    {
        var preferred = new List<string>();
        foreach (var section in ReadIni(Path.Combine(root, "installs.ini")))
            if (section.TryGetValue("Default", out var path)) AddProfile(path, true);
        var profiles = ReadIni(Path.Combine(root, "profiles.ini"));
        foreach (var section in profiles)
            if (section.GetValueOrDefault("Default") == "1" && section.TryGetValue("Path", out var path))
                AddProfile(path, section.GetValueOrDefault("IsRelative") != "0");
        var remaining = new List<string>();
        foreach (var section in profiles)
            if (section.TryGetValue("Path", out var path))
            {
                var resolved = ResolveProfile(path, section.GetValueOrDefault("IsRelative") != "0");
                if (resolved != null) remaining.Add(resolved);
            }
        var directory = Path.Combine(root, "Profiles");
        if (Directory.Exists(directory)) remaining.AddRange(Directory.EnumerateDirectories(directory));
        return preferred.Concat(remaining.OrderBy(Path.GetFileName, StringComparer.OrdinalIgnoreCase)
            .ThenBy(path => path, StringComparer.OrdinalIgnoreCase)).Distinct(StringComparer.OrdinalIgnoreCase).ToArray();

        void AddProfile(string path, bool relative)
        {
            var resolved = ResolveProfile(path, relative);
            if (resolved != null) preferred.Add(resolved);
        }

        string? ResolveProfile(string path, bool relative)
        {
            try
            {
                var resolved = Path.GetFullPath(relative ? Path.Combine(root, path) : path);
                return Directory.Exists(resolved) ? resolved : null;
            }
            catch (Exception ex) when (ex is ArgumentException or NotSupportedException or IOException or UnauthorizedAccessException)
            {
                return null;
            }
        }
    }

    private static List<Dictionary<string, string>> ReadIni(string path)
    {
        var sections = new List<Dictionary<string, string>>();
        try
        {
            Dictionary<string, string>? section = null;
            foreach (var raw in File.ReadAllLines(path))
            {
                var line = raw.Trim();
                if (line.StartsWith(';') || line.StartsWith('#')) continue;
                if (line.StartsWith('[') && line.EndsWith(']'))
                {
                    section = new(StringComparer.OrdinalIgnoreCase);
                    sections.Add(section);
                }
                else if (section != null && line.IndexOf('=') is var equals && equals > 0)
                    section[line[..equals].Trim()] = line[(equals + 1)..].Trim();
            }
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException) { }
        return sections;
    }

    private static byte[] DefaultUnprotect(byte[] bytes)
    {
#if WINDOWS
        return ProtectedData.Unprotect(bytes, null, DataProtectionScope.CurrentUser);
#else
        throw new PlatformNotSupportedException("Windows browser decryption requires Windows.");
#endif
    }
}

public class WindowsBrowserCookieReader : IBrowserCookieReader
{
    private static readonly BrowserCookieQuery CursorCookies = new(
        "Cursor",
        "(host_key = 'cursor.com' OR host_key = 'cursor.sh' OR host_key LIKE '%.cursor.com' OR host_key LIKE '%.cursor.sh') " +
        "AND name IN ('WorkosCursorSessionToken', '__Secure-next-auth.session-token', 'next-auth.session-token')",
        "(host = 'cursor.com' OR host = 'cursor.sh' OR host LIKE '%.cursor.com' OR host LIKE '%.cursor.sh') " +
        "AND name IN ('WorkosCursorSessionToken', '__Secure-next-auth.session-token', 'next-auth.session-token')");

    private static readonly BrowserCookieQuery GrokCookies = new(
        "Grok",
        "(host_key = 'grok.com' OR host_key LIKE '%.grok.com') AND name = 'sso'",
        "(host = 'grok.com' OR host LIKE '%.grok.com') AND name = 'sso'");

    private readonly DebugService? _debugService;
    private readonly WindowsBrowserCookieReaderOptions _options;

    public WindowsBrowserCookieReader(DebugService? debugService = null)
        : this(WindowsBrowserCookieReaderOptions.ForCurrentUser(), debugService)
    {
    }

    public WindowsBrowserCookieReader(WindowsBrowserCookieReaderOptions options, DebugService? debugService = null)
    {
        _options = options ?? throw new ArgumentNullException(nameof(options));
        _debugService = debugService;
    }

    public virtual string? ReadCursorCookieHeader() => ReadCursorCookies().Cookie;
    public virtual string? ReadGrokCookieHeader() => ReadGrokCookies().Cookie;
    public BrowserCookieReadResult ReadCursorCookies() => ReadCookies(CursorCookies);
    public BrowserCookieReadResult ReadGrokCookies() => ReadCookies(GrokCookies);

    private BrowserCookieReadResult ReadCookies(BrowserCookieQuery query)
    {
        string? cookie = null;
        string? source = null;
        var checks = new List<BrowserCookieCheck>();
        foreach (var root in _options.ChromiumRoots.OrderBy(root => root.Name switch { "Chrome" => 0, "Edge" => 1, "Brave" => 2, _ => 3 }))
        {
            if (!Directory.Exists(root.UserDataPath)) continue;
            var result = ReadSafely(() => ReadChromiumCookies(root, query));
            AddResult(root.Name, result);
        }
        var firefox = ReadSafely(() =>
        {
            var profiles = _options.FirefoxProfiles();
            if (profiles.Count == 0 && !(_options.FirefoxInstalled?.Invoke() ?? false)) return new CookieResult(null, "absent");
            var result = new CookieResult(null, "signed_out");
            foreach (var profile in profiles)
                result = Merge(result, ReadSafely(() => ReadDatabase(Path.Combine(profile, "cookies.sqlite"), [], query, true)));
            return result;
        });
        if (firefox.Status != "absent") AddResult("Firefox", firefox);
        return new(cookie, source, checks);

        void AddResult(string name, CookieResult result)
        {
            checks.Add(new(name, result.Status));
            _debugService?.LogInfo(query.Provider, $"Browser {name}: {result.Status}");
            if (cookie == null && result.Cookie != null)
            {
                cookie = result.Cookie;
                source = name;
            }
        }
    }

    private CookieResult ReadChromiumCookies(BrowserCookieRoot root, BrowserCookieQuery query)
    {
        byte[] masterKey;
        try
        {
            masterKey = GetChromiumMasterKey(root.UserDataPath);
        }
        catch
        {
            masterKey = [];
        }
        var result = new CookieResult(null, "signed_out");
        foreach (var cookiePath in EnumerateCookieDatabases(root.UserDataPath))
            result = Merge(result, ReadSafely(() => ReadDatabase(cookiePath, masterKey, query, false)));
        return result;
    }

    private CookieResult ReadDatabase(string cookieDbPath, byte[] masterKey, BrowserCookieQuery query, bool firefox)
    {
        if (!File.Exists(cookieDbPath)) return new(null, "signed_out");
        using var snapshot = _options.SnapshotFactory != null ? _options.SnapshotFactory(cookieDbPath)
            : BrowserCookieDatabaseSnapshot.Create(cookieDbPath, _options.TemporaryRoot);
        using var connection = OpenReadOnlyCookieDatabase(snapshot.DatabasePath);
        connection.Open();
        using var command = connection.CreateCommand();
        command.CommandText = firefox
            ? $"SELECT host, name, value, expiry FROM moz_cookies WHERE {query.FirefoxWhere} ORDER BY LENGTH(host) DESC, expiry DESC, lastAccessed DESC"
            : $"SELECT host_key, name, encrypted_value, expires_utc FROM cookies WHERE {query.ChromiumWhere} ORDER BY LENGTH(host_key) DESC, expires_utc DESC";
        var now = firefox ? _options.UtcNow().ToUnixTimeSeconds() : ChromiumTimestamp(_options.UtcNow());
        using var reader = command.ExecuteReader();
        var cookies = new Dictionary<string, string>(StringComparer.Ordinal);
        var expired = false;
        var active = false;
        var encrypted = false;
        while (reader.Read())
        {
            var expiry = reader.GetInt64(3);
            if (expiry != 0 && expiry <= now) { expired = true; continue; }
            active = true;
            var name = reader.GetString(1);
            if (cookies.ContainsKey(name)) continue;
            string? value;
            if (firefox) value = reader.GetString(2);
            else
            {
                var encryptedValue = (byte[])reader[2];
                try { value = DecryptCookieValue(encryptedValue, masterKey); }
                catch { value = null; }
                if (value == null) encrypted = true;
            }
            if (!string.IsNullOrWhiteSpace(value)) cookies[name] = value;
        }
        return cookies.Count > 0
            ? new(string.Join("; ", cookies.Select(x => $"{x.Key}={x.Value}")), "signed_in")
            : new(null, encrypted ? "encrypted" : expired && !active ? "expired" : "signed_out");
    }

    private static CookieResult ReadSafely(Func<CookieResult> read)
    {
        try { return read(); }
        catch (Exception ex)
        {
            var locked = ex is UnauthorizedAccessException ||
                ex is IOException && (ex.HResult & 0xffff) is 5 or 13 or 32 or 33 ||
                ex is SqliteException sqlite && sqlite.SqliteErrorCode is 3 or 5 or 6 or 23;
            return new(null, locked ? "locked" : "unreadable");
        }
    }

    private static CookieResult Merge(CookieResult first, CookieResult next) =>
        first.Cookie != null || StatusRank(first.Status) >= StatusRank(next.Status) ? first : next;

    private static int StatusRank(string status) => status switch
    {
        "signed_in" => 6, "encrypted" => 5, "locked" => 4, "expired" => 3, "unreadable" => 2, _ => 1
    };

    private sealed record CookieResult(string? Cookie, string Status);

    private static SqliteConnection OpenReadOnlyCookieDatabase(string path)
    {
        var builder = new SqliteConnectionStringBuilder { DataSource = path, Mode = SqliteOpenMode.ReadOnly, Pooling = false };
        return new SqliteConnection(builder.ToString());
    }

    private byte[] GetChromiumMasterKey(string userDataPath)
    {
        var localStatePath = Path.Combine(userDataPath, "Local State");
        if (!File.Exists(localStatePath)) return [];
        using var doc = JsonDocument.Parse(File.ReadAllText(localStatePath));
        var encoded = doc.RootElement.GetProperty("os_crypt").GetProperty("encrypted_key").GetString();
        if (string.IsNullOrWhiteSpace(encoded)) return [];
        var encryptedKey = Convert.FromBase64String(encoded);
        if (encryptedKey.Length <= 5 || !encryptedKey.AsSpan(0, 5).SequenceEqual("DPAPI"u8)) return [];
        return _options.Unprotect(encryptedKey.AsSpan(5).ToArray());
    }

    private string? DecryptCookieValue(byte[] encryptedValue, byte[] masterKey)
    {
        if (encryptedValue.Length == 0) return string.Empty;
        var prefix = encryptedValue.Length >= 3 ? Encoding.ASCII.GetString(encryptedValue, 0, 3) : string.Empty;
        if (prefix == "v20") return null;
        if (prefix is "v10" or "v11")
        {
            if (encryptedValue.Length < 31 || masterKey.Length is not (16 or 24 or 32)) return null;
            try
            {
                var nonce = encryptedValue.AsSpan(3, 12);
                var cipherText = encryptedValue.AsSpan(15, encryptedValue.Length - 31);
                var tag = encryptedValue.AsSpan(encryptedValue.Length - 16, 16);
                var plainText = new byte[cipherText.Length];
                using var aesGcm = new AesGcm(masterKey, 16);
                aesGcm.Decrypt(nonce, cipherText, tag, plainText);
                return Encoding.UTF8.GetString(plainText);
            }
            catch (CryptographicException)
            {
                return null;
            }
        }
        try
        {
            return Encoding.UTF8.GetString(_options.Unprotect(encryptedValue));
        }
        catch (CryptographicException)
        {
            return null;
        }
    }

    private static IReadOnlyList<string> EnumerateCookieDatabases(string userDataPath) =>
        Directory.EnumerateDirectories(userDataPath)
            .Where(path => IsSupportedProfile(Path.GetFileName(path)))
            .OrderBy(path => ProfileRank(Path.GetFileName(path)))
            .ThenBy(path => Path.GetFileName(path), StringComparer.OrdinalIgnoreCase)
            .SelectMany(path => new[] { Path.Combine(path, "Network", "Cookies"), Path.Combine(path, "Cookies") })
            .Where(File.Exists)
            .ToArray();

    private static bool IsSupportedProfile(string name) =>
        name.Equals("Default", StringComparison.OrdinalIgnoreCase) ||
        name.StartsWith("Profile ", StringComparison.OrdinalIgnoreCase) ||
        name.StartsWith("Guest Profile", StringComparison.OrdinalIgnoreCase);

    private static int ProfileRank(string name) => name.Equals("Default", StringComparison.OrdinalIgnoreCase) ? 0
        : name.StartsWith("Profile ", StringComparison.OrdinalIgnoreCase) ? 1 : 2;

    private static long ChromiumTimestamp(DateTimeOffset time) =>
        checked((time.ToUnixTimeSeconds() + 11_644_473_600L) * 1_000_000L);

    private sealed record BrowserCookieQuery(string Provider, string ChromiumWhere, string FirefoxWhere);
}
