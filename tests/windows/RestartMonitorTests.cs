using ClaudeUsageWidget.Services;

internal sealed partial class ServerProcessManagerTests
{
    public async Task Test_RestartAttempts_when_UnexpectedExit()
    {
        foreach (var succeedsOn in new[] { 2, 3, 0 })
        {
            var deps = FakeDependencies.CreateReadyToLaunch();
            deps.Health.Results.Enqueue(true);
            await using var manager = new ServerProcessManager(new ServerProcessOptions(MaxRestartAttempts: 3), deps.Value);
            await manager.EnsureStartedAsync();
            deps.Delay.OnDelay = attempt =>
            {
                deps.Job.ThrowOnAssign = attempt != succeedsOn;
                return Task.CompletedTask;
            };

            deps.Launcher.LastProcess?.RaiseExited();
            var result = await manager.EnsureStartedAsync();

            AssertEqual(succeedsOn == 0 ? ServerProcessState.Failed : ServerProcessState.Started, result.State);
            AssertEqual(succeedsOn == 0 ? ServerProcessError.RestartFailed : ServerProcessError.None, result.Error);
            var attempts = succeedsOn == 0 ? 3 : succeedsOn;
            AssertEqual(1 + attempts, deps.Launcher.Starts);
            AssertEqual(string.Join(',', Enumerable.Range(1, attempts).Select(i => i * 50)),
                string.Join(',', deps.Delay.Delays.Select(delay => delay.TotalMilliseconds)));
        }
    }

    public async Task Test_RestartStops_when_LifecycleChangesBetweenAttempts()
    {
        foreach (var action in new[] { "stop", "ensure", "dispose" })
        {
            var deps = FakeDependencies.CreateReadyToLaunch();
            await using var manager = new ServerProcessManager(new ServerProcessOptions(), deps.Value);
            await manager.EnsureStartedAsync();
            deps.Job.ThrowOnAssign = true;
            var entered = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
            var release = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
            deps.Delay.OnDelay = async attempt =>
            {
                if (attempt != 2) return;
                deps.Job.ThrowOnAssign = false;
                deps.Health.Results.Enqueue(false);
                deps.Health.Results.Enqueue(true);
                if (action == "stop") await manager.StopOwnedServerAsync(CancellationToken.None);
                if (action == "ensure") AssertEqual(ServerProcessState.Started, (await manager.EnsureStartedAsync()).State);
                entered.TrySetResult();
                if (action == "dispose") await release.Task;
            };
            deps.Launcher.LastProcess?.RaiseExited();
            await entered.Task.WaitAsync(TimeSpan.FromSeconds(1));

            if (action == "dispose")
            {
                await manager.DisposeAsync().AsTask().WaitAsync(TimeSpan.FromSeconds(1));
                release.TrySetResult();
            }

            AssertEqual(action == "ensure" ? 3 : 2, deps.Launcher.Starts);
        }
    }

    public async Task Test_RestartMonitorFailure_when_AcquirerThrows()
    {
        var deps = FakeDependencies.CreateReadyToLaunch();
        await using var manager = new ServerProcessManager(new ServerProcessOptions(MaxRestartAttempts: 1), deps.Value);
        await manager.EnsureStartedAsync();
        deps.Version.MatchesQueue.Enqueue(false);
        deps.Acquirer.ThrowException = true;

        deps.Launcher.LastProcess?.RaiseExited();
        await deps.Acquirer.CalledAgain.Task.WaitAsync(TimeSpan.FromSeconds(1));
        var result = await manager.EnsureStartedAsync();

        AssertEqual(ServerProcessState.Failed, result.State);
        AssertEqual(ServerProcessError.RestartFailed, result.Error);
        AssertEqual(1, deps.Launcher.Starts);
        await manager.DisposeAsync();
        await manager.DisposeAsync();
    }

    public async Task Test_RestartMonitorFailure_when_ReadinessThrows()
    {
        var deps = FakeDependencies.CreateReadyToLaunch();
        deps.Health.ThrowAfterCalls = 3;
        await using var manager = new ServerProcessManager(new ServerProcessOptions(MaxRestartAttempts: 1), deps.Value);
        await manager.EnsureStartedAsync();

        deps.Launcher.LastProcess?.RaiseExited();
        await deps.Health.ThrowObserved.Task.WaitAsync(TimeSpan.FromSeconds(1));
        var result = await manager.EnsureStartedAsync();

        AssertEqual(ServerProcessState.Failed, result.State);
        AssertEqual(ServerProcessError.RestartFailed, result.Error);
        AssertEqual(2, deps.Launcher.Starts);
        AssertEqual(true, deps.Launcher.LastProcess?.Killed);
        AssertEqual(true, deps.Launcher.LastProcess?.Disposed);
        AssertEqual(true, deps.Job.LastJob?.Disposed);
        await manager.DisposeAsync();
        await manager.DisposeAsync();
    }
}
