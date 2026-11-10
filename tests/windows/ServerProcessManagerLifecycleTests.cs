using ClaudeUsageWidget.Services;

internal sealed partial class ServerProcessManagerTests
{
    public Task Test_LauncherUsesListenAddrFlag_when_BuildingStartInfo()
    {
        var startInfo = ServerProcessLauncher.BuildStartInfo(new ServerLaunchRequest("usage-server.exe", 7823));

        AssertEqual("usage-server.exe", startInfo.FileName);
        AssertEqual(false, startInfo.UseShellExecute);
        AssertEqual(true, startInfo.CreateNoWindow);
        AssertEqual("--listen-addr", startInfo.ArgumentList[0]);
        AssertEqual("127.0.0.1:7823", startInfo.ArgumentList[1]);
        return Task.CompletedTask;
    }

    public async Task Test_AssignerThrow_when_ProcessAlreadyLaunched()
    {
        var deps = FakeDependencies.CreateReadyToLaunch();
        deps.Job.ThrowOnAssign = true;
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);

        var result = await manager.EnsureStartedAsync();

        AssertEqual(ServerProcessState.Failed, result.State);
        AssertEqual(ServerProcessError.JobAssignmentFailed, result.Error);
        AssertEqual(true, deps.Launcher.LastProcess?.Killed);
        AssertEqual(true, deps.Launcher.LastProcess?.Disposed);
    }

    public async Task Test_IsAttachedClears_when_AttachedStateEnds()
    {
        foreach (var action in new[] { "dispose", "stop", "fail" })
        {
            var deps = FakeDependencies.Create();
            deps.Health.Results.Enqueue(true);
            await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);
            await manager.EnsureStartedAsync();
            AssertEqual(true, manager.IsAttached);

            if (action == "dispose") await manager.DisposeAsync();
            if (action == "stop") await manager.StopOwnedServerAsync(CancellationToken.None);
            if (action == "fail")
            {
                deps.Version.MatchesQueue.Enqueue(false);
                AssertEqual(ServerProcessState.Failed, (await manager.EnsureStartedAsync()).State);
            }

            AssertEqual(false, manager.IsAttached);
            AssertEqual(false, manager.OwnsProcess);
            AssertEqual(0, deps.Launcher.Starts);
        }
    }

    public async Task Test_DisposeRace_when_ExitEventArrives()
    {
        var deps = FakeDependencies.CreateReadyToLaunch();
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);
        await manager.EnsureStartedAsync();

        var dispose = manager.DisposeAsync().AsTask();
        deps.Launcher.LastProcess?.RaiseExited();
        await dispose.WaitAsync(TimeSpan.FromSeconds(1));

        AssertEqual(1, deps.Launcher.Starts);
        var afterDispose = await manager.EnsureStartedAsync();
        AssertEqual(ServerProcessState.Failed, afterDispose.State);
        AssertEqual(ServerProcessError.Disposed, afterDispose.Error);
    }

    public async Task Test_StaleExitEvent_when_ProcessWasRestartedManually()
    {
        var deps = FakeDependencies.CreateReadyToLaunch();
        deps.Health.Results.Enqueue(true);
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);
        await manager.EnsureStartedAsync();
        var oldProcess = deps.Launcher.LastProcess;

        await manager.RestartOwnedAsync();
        oldProcess?.RaiseExited();
        await Task.Delay(20);

        AssertEqual(2, deps.Launcher.Starts);
    }

    public async Task Test_AcquisitionCancellation_when_BeforeLaunch()
    {
        var deps = FakeDependencies.Create();
        deps.Health.Results.Enqueue(false);
        deps.Version.MatchesQueue.Enqueue(false);
        deps.Acquirer.ThrowCancellation = true;
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);

        await AssertCanceled(() => manager.EnsureStartedAsync());

        AssertEqual(0, deps.Launcher.Starts);
    }

    public async Task Test_ReadinessCancellation_when_AfterLaunch()
    {
        var deps = FakeDependencies.Create();
        deps.Health.Results.Enqueue(false);
        deps.Health.ThrowCancellationAfterCalls = 2;
        using var cancellation = new CancellationTokenSource();
        deps.Health.CancelWhenThrowing = cancellation;
        deps.Version.MatchesQueue.Enqueue(true);
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);

        await AssertCanceled(() => manager.EnsureStartedAsync(cancellation.Token));

        AssertEqual(true, deps.Launcher.LastProcess?.Killed);
        AssertEqual(true, deps.Launcher.LastProcess?.Disposed);
        AssertEqual(true, deps.Job.LastJob?.Disposed);
        AssertEqual(1, deps.Launcher.Starts);
    }

    public async Task Test_ReadinessException_when_AfterLaunch()
    {
        var deps = FakeDependencies.Create();
        deps.Health.Results.Enqueue(false);
        deps.Health.ThrowAfterCalls = 2;
        deps.Version.MatchesQueue.Enqueue(true);
        await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);

        var result = await manager.EnsureStartedAsync();

        AssertEqual(ServerProcessState.Failed, result.State);
        AssertEqual(ServerProcessError.ReadinessFailed, result.Error);
        AssertEqual(true, deps.Launcher.LastProcess?.Killed);
        AssertEqual(true, deps.Launcher.LastProcess?.Disposed);
        AssertEqual(true, deps.Job.LastJob?.Disposed);
    }
}
