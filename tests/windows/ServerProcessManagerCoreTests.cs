using ClaudeUsageWidget.Services;

internal sealed partial class ServerProcessManagerTests
{
    public async Task Test_RemoteMode_when_ApiUrlConfigured()
    {
        var deps = FakeDependencies.Create();
        await using var manager = new ServerProcessManager(new ServerProcessOptions(ApiUrl: "http://localhost:9000/api"), deps.Value);

        var result = await manager.EnsureStartedAsync();

        AssertEqual(ServerProcessState.Remote, result.State);
        AssertEqual("http://localhost:9000/api/", result.EffectiveBaseUrl.ToString());
        AssertEqual(0, deps.Health.Calls);
        AssertEqual(0, deps.Launcher.Starts);
        AssertEqual(0, deps.Acquirer.Calls);
    }

    public async Task Test_AttachFirst_when_DefaultHealthHealthy()
    {
        var deps = FakeDependencies.Create();
        deps.Health.Results.Enqueue(true);
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);

        var result = await manager.EnsureStartedAsync();

        AssertEqual(ServerProcessState.Attached, result.State);
        AssertEqual(false, result.OwnsProcess);
        AssertEqual(true, manager.IsAttached);
        AssertEqual(0, deps.Launcher.Starts);
    }

    public async Task Test_ReprobesAttachedServer_when_EnsuringStartedAgain()
    {
        var deps = FakeDependencies.Create();
        foreach (var healthy in new[] { true, true, false, true })
            deps.Health.Results.Enqueue(healthy);
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);
        await manager.EnsureStartedAsync();

        AssertEqual(ServerProcessState.Attached, (await manager.EnsureStartedAsync()).State);
        AssertEqual(2, deps.Health.Calls);
        AssertEqual(0, deps.Launcher.Starts);
        AssertEqual(ServerProcessState.Started, (await manager.EnsureStartedAsync()).State);
        AssertEqual(1, deps.Launcher.Starts);
        AssertEqual(ServerProcessState.Started, (await manager.EnsureStartedAsync()).State);
        AssertEqual(4, deps.Health.Calls);
    }

    public async Task Test_AcquiresBinary_when_MissingBeforeSpawn()
    {
        var deps = FakeDependencies.Create();
        deps.Health.Results.Enqueue(false);
        deps.Health.Results.Enqueue(true);
        deps.Version.MatchesQueue.Enqueue(false);
        deps.Version.MatchesQueue.Enqueue(true);
        deps.Acquirer.Result = true;
        await using var manager = new ServerProcessManager(new ServerProcessOptions(ExecutableDirectory: "/tmp"), deps.Value);

        var result = await manager.EnsureStartedAsync();

        AssertEqual(ServerProcessState.Started, result.State);
        AssertEqual(1, deps.Acquirer.Calls);
        AssertEqual(1, deps.Launcher.Starts);
        AssertEqual("version,acquire,version,launch,job", string.Join(',', deps.Order));
    }

    public async Task Test_StartsOwned_when_HealthBecomesReady()
    {
        var deps = FakeDependencies.CreateReadyToLaunch();
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);

        var result = await manager.EnsureStartedAsync();

        AssertEqual(ServerProcessState.Started, result.State);
        AssertEqual(true, result.OwnsProcess);
        AssertEqual(1, deps.Job.Assigns);
    }

    public async Task Test_CleansUpOwnedProcess_when_ReadinessFails()
    {
        var deps = FakeDependencies.Create();
        deps.Health.Results.Enqueue(false);
        deps.Version.MatchesQueue.Enqueue(true);
        await using var manager = new ServerProcessManager(new ServerProcessOptions(MaxReadinessAttempts: 2), deps.Value);

        var result = await manager.EnsureStartedAsync();

        AssertEqual(ServerProcessState.Failed, result.State);
        AssertEqual(ServerProcessError.ReadinessTimedOut, result.Error);
        AssertEqual(true, deps.Launcher.LastProcess?.Disposed);
        AssertEqual(true, deps.Job.LastJob?.Disposed);
    }

    public async Task Test_ConcurrentEnsure_when_LocalStartNeeded()
    {
        var deps = FakeDependencies.CreateReadyToLaunch();
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);

        var results = await Task.WhenAll(manager.EnsureStartedAsync(), manager.EnsureStartedAsync(), manager.EnsureStartedAsync());

        AssertEqual(1, deps.Launcher.Starts);
        AssertEqual(3, results.Length);
        AssertEqual(true, results.All(result => result.State == ServerProcessState.Started));
    }

    public async Task Test_RestartsOwnedProcess_when_ItExits()
    {
        var deps = FakeDependencies.CreateReadyToLaunch();
        deps.Health.Results.Enqueue(true);
        deps.Launcher.SecondStarted = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);
        await manager.EnsureStartedAsync();

        deps.Launcher.LastProcess?.RaiseExited();
        await deps.Launcher.SecondStarted.Task.WaitAsync(TimeSpan.FromSeconds(1));

        AssertEqual(2, deps.Launcher.Starts);
    }

    public async Task Test_DoesNotRestart_when_Attached()
    {
        var deps = FakeDependencies.Create();
        deps.Health.Results.Enqueue(true);
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);

        await manager.EnsureStartedAsync();
        await manager.RestartOwnedAsync();

        AssertEqual(0, deps.Launcher.Starts);
    }

    public async Task Test_Dispose_when_OwnedAndAttached()
    {
        var owned = FakeDependencies.CreateReadyToLaunch();
        await using (var manager = new ServerProcessManager(new ServerProcessOptions(), owned.Value))
        {
            await manager.EnsureStartedAsync();
        }
        AssertEqual(true, owned.Launcher.LastProcess?.Disposed);
        AssertEqual(true, owned.Job.LastJob?.Disposed);

        var attached = FakeDependencies.Create();
        attached.Health.Results.Enqueue(true);
        await using (var manager = new ServerProcessManager(new ServerProcessOptions(), attached.Value))
        {
            await manager.EnsureStartedAsync();
        }
        AssertEqual(0, attached.Launcher.Starts);
    }
}
