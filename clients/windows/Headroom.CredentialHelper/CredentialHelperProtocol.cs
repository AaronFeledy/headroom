using System.Text.Json;
using ClaudeUsageWidget.Services;

namespace Headroom.CredentialHelper;

public static class CredentialHelperProtocol
{
    public const int MaximumOutputBytes = 64 * 1024;

    public static bool IsKnownInvocation(IReadOnlyList<string> arguments) =>
        arguments.Count == 1 && arguments[0].Trim().ToLowerInvariant() is "cursor" or "grok";

    public static int Run(IReadOnlyList<string> arguments, IBrowserCookieReader reader, TextWriter output)
    {
        if (!IsKnownInvocation(arguments)) return 2;
        var provider = arguments[0].Trim().ToLowerInvariant();
        var result = provider switch
        {
            "cursor" => reader.ReadCursorCookies(),
            "grok" => reader.ReadGrokCookies(),
            _ => throw new InvalidOperationException()
        };
        var json = JsonSerializer.Serialize(new { provider, cookie = result.Cookie, source = result.Cookie == null ? null : result.Source,
            @checked = result.Checked.Select(check => new { name = check.Name, status = check.Status }) });
        if (System.Text.Encoding.UTF8.GetByteCount(json) > MaximumOutputBytes) return 3;
        output.Write(json);
        return 0;
    }
}
