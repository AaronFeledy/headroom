using System.Net;
using System.Text;
using ClaudeUsageWidget.Models;
using ClaudeUsageWidget.Services;

internal sealed partial class TrayApiHarnessTests
{
    public Task Test_SettingsMigration_preservesValuesAndBackup()
    {
        var dir = Directory.CreateTempSubdirectory();
        try
        {
            var path = Path.Combine(dir.FullName, "settings.json");
            var original = "{\"RefreshIntervalSeconds\":120,\"StartWithWindows\":true,\"NotificationsEnabled\":false,\"DebugMode\":true,\"PrimaryProvider\":\"Cursor\",\"Unknown\":{\"nested\":1}}";
            File.WriteAllText(path, original);

            var settings = new SettingsService(path);

            AssertEqual(120, settings.Settings.RefreshIntervalSeconds);
            AssertEqual(true, settings.Settings.StartWithWindows);
            AssertEqual(false, settings.Settings.NotificationsEnabled);
            AssertEqual(true, settings.Settings.DebugMode);
            AssertEqual("Cursor", settings.Settings.PrimaryProvider);
            AssertEqual(string.Empty, settings.Settings.ApiUrl);
            AssertEqual(string.Empty, settings.Settings.ApiToken);
            AssertEqual(original, File.ReadAllText(path + ".bak"));
            AssertEqual(true, File.ReadAllText(path).Contains("\"Unknown\""));
            return Task.CompletedTask;
        }
        finally
        {
            dir.Delete(recursive: true);
        }
    }

    public async Task Test_OfflineRetainsLastGoodAndRetries()
    {
        var handler = new QueueHandler(SuccessUsage("Claude", 90), null);
        await using var manager = RemoteManager();
        var poller = Poller(handler, manager);

        var first = await poller.PollAsync();
        var second = await poller.PollAsync();

        AssertEqual(TrayApiState.Ready, first.State);
        AssertEqual(TrayApiState.Offline, second.State);
        AssertEqual(1, second.RetryAttempt);
        AssertEqual(true, second.IsStale);
        AssertEqual(90f, second.Providers[0].Current.Utilization);
    }

    public async Task Test_OfflineSnapshotPreservesIndependentBucketCopies()
    {
        var handler = new QueueHandler(SuccessUsageWithBuckets(), null);
        await using var manager = RemoteManager();
        var poller = Poller(handler, manager);

        var ready = await poller.PollAsync();
        ready.Providers[0].Buckets[0].Label = "mutated";
        var stale = await poller.PollAsync();

        AssertEqual(1, stale.Providers[0].Buckets.Count);
        AssertEqual("five-hour", stale.Providers[0].Buckets[0].Id);
        AssertEqual("5-hour window", stale.Providers[0].Buckets[0].Label);
        AssertEqual(37.5f, stale.Providers[0].Buckets[0].Utilization);
        AssertEqual(new DateTime(2026, 7, 16, 12, 30, 0, DateTimeKind.Utc), stale.Providers[0].Buckets[0].ResetsAt);
    }

    public async Task Test_UnauthorizedDistinctFromOffline()
    {
        var handler = new QueueHandler(SuccessUsage("Claude", 42), new HttpResponseMessage(HttpStatusCode.Unauthorized));
        await using var manager = RemoteManager();
        var poller = Poller(handler, manager);

        await poller.PollAsync();
        var snapshot = await poller.PollAsync();

        AssertEqual(TrayApiState.Unauthorized, snapshot.State);
        AssertEqual(0, snapshot.RetryAttempt);
        AssertEqual(true, snapshot.IsStale);
        AssertEqual(42f, snapshot.Providers[0].Current.Utilization);
    }

    public async Task Test_LocalOwnedOfflineDoesNotDoubleSpawnHealthyChild()
    {
        var deps = FakeDependencies.CreateReadyToLaunch();
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);
        var poller = Poller(new QueueHandler((HttpResponseMessage?)null), manager);

        var snapshot = await poller.PollAsync();

        AssertEqual(TrayApiState.Offline, snapshot.State);
        AssertEqual(1, deps.Launcher.Starts);
    }

    public async Task Test_RestartFailedOfflineRequestsRecovery()
    {
        var deps = FakeDependencies.CreateReadyToLaunch();
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);
        await manager.EnsureStartedAsync();
        deps.Health.ThrowAfterCalls = 3;
        deps.Launcher.LastProcess?.RaiseExited();
        await deps.Health.ThrowObserved.Task.WaitAsync(TimeSpan.FromSeconds(1));
        deps.Health.ThrowAfterCalls = 0;
        deps.Health.Results.Enqueue(true);
        var poller = Poller(new QueueHandler((HttpResponseMessage?)null), manager);

        var snapshot = await poller.PollAsync();

        AssertEqual(TrayApiState.Offline, snapshot.State);
        AssertEqual(5, deps.Launcher.Starts);
    }

    public async Task Test_RemoteModeSkipsLocalProcessWork()
    {
        var deps = FakeDependencies.Create();
        await using var manager = new ServerProcessManager(new ServerProcessOptions(ApiUrl: "http://remote.test:9000"), deps.Value);
        var poller = Poller(new QueueHandler(SuccessUsage("Claude", 1)), manager, new Uri("http://remote.test:9000/"));

        var snapshot = await poller.PollAsync();

        AssertEqual(TrayApiState.Ready, snapshot.State);
        AssertEqual(0, deps.Health.Calls);
        AssertEqual(0, deps.Launcher.Starts);
        AssertEqual(0, deps.Acquirer.Calls);
    }

    public async Task Test_CursorCookiePutDoesNotLogSecret()
    {
        const string secret = "WorkosCursorSessionToken=super-secret";
        var debug = new DebugService();
        var handler = new QueueHandler(CursorNeedsAuth(), CursorPutSuccess());
        await using var manager = RemoteManager();
        var poller = Poller(handler, manager, cookieReader: new FakeCookieReader(secret), debug: debug);

        var snapshot = await poller.PollAsync();

        AssertEqual(TrayApiState.Ready, snapshot.State);
        AssertEqual(true, handler.PutBody?.Contains(secret) == true);
        AssertEqual(false, debug.GetEntries().Any(entry => entry.ToString().Contains("super-secret", StringComparison.Ordinal)));
    }

    public async Task Test_CursorMissingSessionPushesCookieOnce()
    {
        const string secret = "WorkosCursorSessionToken=real-missing-secret";
        var handler = new QueueHandler(CursorMissingSession(), CursorPutSuccess());
        var reader = new FakeCookieReader(secret);
        await using var manager = RemoteManager();
        var poller = Poller(handler, manager, cookieReader: reader);

        var snapshot = await poller.PollAsync();

        AssertEqual(TrayApiState.Ready, snapshot.State);
        AssertEqual(1, reader.Calls);
        AssertEqual(1, handler.PutRequests);
        AssertEqual(7f, snapshot.Providers[0].Current.Utilization);
    }

    public async Task Test_CursorExpiredSessionPushesCookieOnce()
    {
        var handler = new QueueHandler(CursorExpiredSession(), CursorPutSuccess());
        var reader = new FakeCookieReader("WorkosCursorSessionToken=expired-secret");
        await using var manager = RemoteManager();
        var poller = Poller(handler, manager, cookieReader: reader);

        var snapshot = await poller.PollAsync();

        AssertEqual(TrayApiState.Ready, snapshot.State);
        AssertEqual(1, reader.Calls);
        AssertEqual(1, handler.PutRequests);
        AssertEqual(7f, snapshot.Providers[0].Current.Utilization);
    }

    public async Task Test_CursorGenericErrorDoesNotPushCookie()
    {
        var handler = new QueueHandler(CursorGenericError());
        var reader = new FakeCookieReader("WorkosCursorSessionToken=generic-secret");
        await using var manager = RemoteManager();
        var poller = Poller(handler, manager, cookieReader: reader);

        var snapshot = await poller.PollAsync();

        AssertEqual(TrayApiState.Ready, snapshot.State);
        AssertEqual(0, reader.Calls);
        AssertEqual(0, handler.PutRequests);
        AssertEqual("Cursor usage request failed with HTTP 500.", snapshot.Providers[0].Error);
    }

    public async Task Test_CursorFailedPushRetainsOriginalState()
    {
        var handler = new QueueHandler(CursorMissingSession(), new HttpResponseMessage(HttpStatusCode.BadGateway));
        var reader = new FakeCookieReader("WorkosCursorSessionToken=failed-push-secret");
        var debug = new DebugService();
        await using var manager = RemoteManager();
        var poller = Poller(handler, manager, cookieReader: reader, debug: debug);

        var snapshot = await poller.PollAsync();

        AssertEqual(TrayApiState.Ready, snapshot.State);
        AssertEqual(1, reader.Calls);
        AssertEqual(1, handler.PutRequests);
        AssertEqual("Log in to cursor.com, or push Cursor credentials from the tray.", snapshot.Providers[0].Error);
        AssertEqual(false, debug.GetEntries().Any(entry => entry.ToString().Contains("failed-push-secret", StringComparison.Ordinal)));
    }

    public async Task Test_GrokMissingWeeklyPushesBrowserCookie()
    {
        const string secret = "sso=grok-session-secret";
        var handler = new QueueHandler(GrokCreditsOnly(), GrokPutSuccess());
        var reader = new FakeCookieReader(null, secret);
        await using var manager = RemoteManager();
        var poller = Poller(handler, manager, cookieReader: reader);

        var snapshot = await poller.PollAsync();

        AssertEqual(TrayApiState.Ready, snapshot.State);
        AssertEqual(1, reader.GrokCalls);
        AssertEqual(1, handler.PutRequests);
        AssertEqual(true, handler.PutBody?.Contains(secret, StringComparison.Ordinal) == true);
        AssertEqual(2, snapshot.Providers[0].Buckets.Count);
        AssertEqual("weekly", snapshot.Providers[0].Buckets[1].Id);
    }

    public async Task Test_GrokCliAuthErrorStillPushesBrowserCookie()
    {
        const string secret = "sso=grok-session-secret";
        var handler = new QueueHandler(GrokNeedsCliAuth(), GrokWeeklyOnlyPutSuccess());
        var reader = new FakeCookieReader(null, secret);
        await using var manager = RemoteManager();
        var poller = Poller(handler, manager, cookieReader: reader);

        var snapshot = await poller.PollAsync();

        AssertEqual(TrayApiState.Ready, snapshot.State);
        AssertEqual(1, reader.GrokCalls);
        AssertEqual(1, handler.PutRequests);
        AssertEqual(null, snapshot.Providers[0].Error);
        AssertEqual("weekly", snapshot.Providers[0].Buckets[0].Id);
    }

    public async Task Test_RemoteHttpDoesNotPushBrowserCredentials()
    {
        var handler = new QueueHandler(GrokCreditsOnly());
        var reader = new FakeCookieReader(null, "sso=must-not-leave-device");
        await using var manager = new ServerProcessManager(new ServerProcessOptions(ApiUrl: "http://remote.test:9000"), FakeDependencies.Create().Value);
        var poller = Poller(handler, manager, new Uri("http://remote.test:9000/"), reader);

        var snapshot = await poller.PollAsync();

        AssertEqual(TrayApiState.Ready, snapshot.State);
        AssertEqual(0, reader.GrokCalls);
        AssertEqual(0, handler.PutRequests);
    }

    public Task Test_DebugServiceRedactsGrokSsoCookie()
    {
        AssertEqual("cookie sso=[redacted]", DebugService.Redact("cookie sso=secret-value"));
        return Task.CompletedTask;
    }

    public async Task Test_GetHealth_returns_server_version()
    {
        var handler = new QueueHandler(SuccessHealth("0.2.1", "ok"));
        var client = new ApiClient(new HttpClient(handler), new TrayApiClientOptions { BaseUrl = new Uri("http://localhost:7823/") });

        var result = await client.GetHealthAsync();

        AssertEqual(true, result.IsSuccess);
        AssertEqual("ok", result.Value!.Status);
        AssertEqual("0.2.1", result.Value.Version);
    }

    public async Task Test_GetHealth_malformed_when_version_missing()
    {
        var handler = new QueueHandler(new HttpResponseMessage(HttpStatusCode.OK)
        {
            Content = new StringContent("{\"status\":\"ok\",\"version\":null,\"providers\":[]}", Encoding.UTF8, "application/json")
        });
        var client = new ApiClient(new HttpClient(handler), new TrayApiClientOptions { BaseUrl = new Uri("http://localhost:7823/") });

        var result = await client.GetHealthAsync();

        AssertEqual(false, result.IsSuccess);
        AssertEqual(ApiResultStatus.MalformedResponse, result.Status);
    }

    public async Task Test_GetHealth_offline_when_unreachable()
    {
        var handler = new QueueHandler((HttpResponseMessage?)null);
        var client = new ApiClient(new HttpClient(handler), new TrayApiClientOptions { BaseUrl = new Uri("http://localhost:7823/") });

        var result = await client.GetHealthAsync();

        AssertEqual(false, result.IsSuccess);
        AssertEqual(ApiResultStatus.Offline, result.Status);
    }

    private static TrayUsagePoller Poller(QueueHandler handler, ServerProcessManager manager, Uri? baseUrl = null, IProviderCookieReader? cookieReader = null, DebugService? debug = null)
    {
        var client = new ApiClient(new HttpClient(handler), new TrayApiClientOptions { BaseUrl = baseUrl ?? new Uri("http://localhost:7823/") });
        return new TrayUsagePoller(client, manager, cookieReader ?? new FakeCookieReader(null), debug ?? new DebugService());
    }

    private static ServerProcessManager RemoteManager() => new(new ServerProcessOptions(ApiUrl: "http://localhost:7823"), FakeDependencies.Create().Value);

    private static HttpResponseMessage SuccessUsage(string provider, float utilization) => new(HttpStatusCode.OK)
    {
        Content = new StringContent($"[{{\"provider_name\":\"{provider}\",\"primary_label\":\"Current\",\"secondary_label\":\"Weekly\",\"show_secondary\":true,\"subtitle\":null,\"primary_status_text\":null,\"secondary_status_text\":null,\"reauth_command\":null,\"current\":{{\"utilization\":{utilization},\"resets_at\":null}},\"weekly\":{{\"utilization\":2,\"resets_at\":null}},\"error\":null,\"needs_reauth\":false,\"is_success\":true}}]", Encoding.UTF8, "application/json")
    };

    private static HttpResponseMessage SuccessUsageWithBuckets() => new(HttpStatusCode.OK)
    {
        Content = new StringContent("[{\"provider_name\":\"Claude\",\"primary_label\":\"Current\",\"secondary_label\":\"Weekly\",\"show_secondary\":true,\"subtitle\":null,\"primary_status_text\":null,\"secondary_status_text\":null,\"reauth_command\":null,\"current\":{\"utilization\":37.5,\"resets_at\":null},\"weekly\":{\"utilization\":2,\"resets_at\":null},\"buckets\":[{\"id\":\"five-hour\",\"label\":\"5-hour window\",\"utilization\":37.5,\"resets_at\":\"2026-07-16T12:30:00Z\"}],\"error\":null,\"needs_reauth\":false,\"is_success\":true}]", Encoding.UTF8, "application/json")
    };

    private static HttpResponseMessage CursorNeedsAuth() => new(HttpStatusCode.OK)
    {
        Content = new StringContent("[{\"provider_name\":\"Cursor\",\"primary_label\":\"Monthly\",\"secondary_label\":\"Monthly\",\"show_secondary\":false,\"subtitle\":null,\"primary_status_text\":null,\"secondary_status_text\":null,\"reauth_command\":\"cursor\",\"current\":{\"utilization\":0,\"resets_at\":null},\"weekly\":{\"utilization\":0,\"resets_at\":null},\"error\":\"needs auth\",\"needs_reauth\":true,\"is_success\":false}]", Encoding.UTF8, "application/json")
    };

    private static HttpResponseMessage CursorMissingSession() => new(HttpStatusCode.OK)
    {
        Content = new StringContent("[{\"provider_name\":\"Cursor\",\"primary_label\":\"Included Plan\",\"secondary_label\":\"On-Demand\",\"show_secondary\":true,\"subtitle\":null,\"primary_status_text\":null,\"secondary_status_text\":null,\"reauth_command\":null,\"current\":{\"utilization\":0,\"resets_at\":null},\"weekly\":{\"utilization\":0,\"resets_at\":null},\"error\":\"Log in to cursor.com, or push Cursor credentials from the tray.\",\"needs_reauth\":false,\"is_success\":false}]", Encoding.UTF8, "application/json")
    };

    private static HttpResponseMessage CursorExpiredSession() => new(HttpStatusCode.OK)
    {
        Content = new StringContent("[{\"provider_name\":\"Cursor\",\"primary_label\":\"Included Plan\",\"secondary_label\":\"On-Demand\",\"show_secondary\":true,\"subtitle\":null,\"primary_status_text\":null,\"secondary_status_text\":null,\"reauth_command\":null,\"current\":{\"utilization\":0,\"resets_at\":null},\"weekly\":{\"utilization\":0,\"resets_at\":null},\"error\":\"Cursor session expired. Log in to cursor.com again.\",\"needs_reauth\":true,\"is_success\":false}]", Encoding.UTF8, "application/json")
    };

    private static HttpResponseMessage CursorGenericError() => new(HttpStatusCode.OK)
    {
        Content = new StringContent("[{\"provider_name\":\"Cursor\",\"primary_label\":\"Included Plan\",\"secondary_label\":\"On-Demand\",\"show_secondary\":true,\"subtitle\":null,\"primary_status_text\":null,\"secondary_status_text\":null,\"reauth_command\":null,\"current\":{\"utilization\":0,\"resets_at\":null},\"weekly\":{\"utilization\":0,\"resets_at\":null},\"error\":\"Cursor usage request failed with HTTP 500.\",\"needs_reauth\":false,\"is_success\":false}]", Encoding.UTF8, "application/json")
    };

    private static HttpResponseMessage CursorPutSuccess() => new(HttpStatusCode.OK)
    {
        Content = new StringContent("{\"provider\":\"Cursor\",\"refetched\":true,\"usage\":{\"provider_name\":\"Cursor\",\"primary_label\":\"Monthly\",\"secondary_label\":\"Monthly\",\"show_secondary\":false,\"subtitle\":null,\"primary_status_text\":null,\"secondary_status_text\":null,\"reauth_command\":null,\"current\":{\"utilization\":7,\"resets_at\":null},\"weekly\":{\"utilization\":0,\"resets_at\":null},\"error\":null,\"needs_reauth\":false,\"is_success\":true}}", Encoding.UTF8, "application/json")
    };

    private static HttpResponseMessage GrokCreditsOnly() => new(HttpStatusCode.OK)
    {
        Content = new StringContent("[{\"provider_name\":\"Grok\",\"primary_label\":\"Credits\",\"secondary_label\":\"Pay as you go\",\"show_secondary\":false,\"subtitle\":null,\"primary_status_text\":null,\"secondary_status_text\":null,\"reauth_command\":null,\"current\":{\"utilization\":15,\"resets_at\":null},\"weekly\":{\"utilization\":0,\"resets_at\":null},\"buckets\":[{\"id\":\"credits\",\"label\":\"Credits\",\"utilization\":15,\"resets_at\":null,\"status_text\":null}],\"error\":null,\"needs_reauth\":false,\"is_success\":true}]", Encoding.UTF8, "application/json")
    };

    private static HttpResponseMessage GrokPutSuccess() => new(HttpStatusCode.OK)
    {
        Content = new StringContent("{\"provider\":\"Grok\",\"refetched\":true,\"usage\":{\"provider_name\":\"Grok\",\"primary_label\":\"Credits\",\"secondary_label\":\"Weekly\",\"show_secondary\":true,\"subtitle\":null,\"primary_status_text\":null,\"secondary_status_text\":null,\"reauth_command\":null,\"current\":{\"utilization\":15,\"resets_at\":null},\"weekly\":{\"utilization\":1,\"resets_at\":\"2026-07-21T18:35:57Z\"},\"buckets\":[{\"id\":\"credits\",\"label\":\"Credits\",\"utilization\":15,\"resets_at\":null,\"status_text\":null},{\"id\":\"weekly\",\"label\":\"Weekly\",\"utilization\":1,\"resets_at\":\"2026-07-21T18:35:57Z\",\"status_text\":null}],\"error\":null,\"needs_reauth\":false,\"is_success\":true}}", Encoding.UTF8, "application/json")
    };

    private static HttpResponseMessage GrokNeedsCliAuth() => new(HttpStatusCode.OK)
    {
        Content = new StringContent("[{\"provider_name\":\"Grok\",\"primary_label\":\"Credits\",\"secondary_label\":\"Pay as you go\",\"show_secondary\":false,\"subtitle\":null,\"primary_status_text\":null,\"secondary_status_text\":null,\"reauth_command\":\"grok login\",\"current\":{\"utilization\":0,\"resets_at\":null},\"weekly\":{\"utilization\":0,\"resets_at\":null},\"buckets\":[],\"error\":\"Grok auth expired\",\"needs_reauth\":true,\"is_success\":false}]", Encoding.UTF8, "application/json")
    };

    private static HttpResponseMessage GrokWeeklyOnlyPutSuccess() => new(HttpStatusCode.OK)
    {
        Content = new StringContent("{\"provider\":\"Grok\",\"refetched\":true,\"usage\":{\"provider_name\":\"Grok\",\"primary_label\":\"Weekly\",\"secondary_label\":\"Weekly\",\"show_secondary\":false,\"subtitle\":null,\"primary_status_text\":null,\"secondary_status_text\":null,\"reauth_command\":\"grok login\",\"current\":{\"utilization\":1,\"resets_at\":\"2026-07-21T18:35:57Z\"},\"weekly\":{\"utilization\":0,\"resets_at\":null},\"buckets\":[{\"id\":\"weekly\",\"label\":\"Weekly\",\"utilization\":1,\"resets_at\":\"2026-07-21T18:35:57Z\",\"status_text\":null}],\"error\":null,\"needs_reauth\":false,\"is_success\":true}}", Encoding.UTF8, "application/json")
    };

    private static HttpResponseMessage SuccessHealth(string version, string status) => new(HttpStatusCode.OK)
    {
        Content = new StringContent($"{{\"status\":\"{status}\",\"version\":\"{version}\",\"providers\":[]}}", Encoding.UTF8, "application/json")
    };

    internal static void AssertEqual<T>(T expected, T actual)
    {
        if (!EqualityComparer<T>.Default.Equals(expected, actual))
        {
            throw new InvalidOperationException($"Expected {expected}, got {actual}");
        }
    }
}

internal sealed class QueueHandler : HttpMessageHandler
{
    private readonly Queue<HttpResponseMessage?> _responses;
    public string? PutBody { get; private set; }
    public int PutRequests { get; private set; }

    public QueueHandler(params HttpResponseMessage?[] responses)
    {
        _responses = new Queue<HttpResponseMessage?>(responses);
    }

    protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        if (request.Method == HttpMethod.Put && request.Content != null)
        {
            PutRequests++;
            PutBody = await request.Content.ReadAsStringAsync(cancellationToken);
        }
        var response = _responses.Count == 0 ? null : _responses.Dequeue();
        return response ?? throw new HttpRequestException("offline");
    }
}

internal sealed class FakeCookieReader : IProviderCookieReader
{
    private readonly string? _cookie;
    private readonly string? _grokCookie;
    public int Calls { get; private set; }
    public int GrokCalls { get; private set; }

    public FakeCookieReader(string? cookie, string? grokCookie = null)
    {
        _cookie = cookie;
        _grokCookie = grokCookie;
    }

    public string? ReadCursorCookieHeader()
    {
        Calls++;
        return _cookie;
    }

    public string? ReadGrokCookieHeader()
    {
        GrokCalls++;
        return _grokCookie;
    }
}
