namespace ClaudeUsageWidget.Services;

public sealed partial class ServerProcessManager : IAsyncDisposable
{
    public static readonly Uri DefaultBaseUrl = new("http://127.0.0.1:7823/");

    private readonly ServerProcessOptions _options;
    private readonly ServerProcessDependencies _dependencies;
    private readonly ServerProcessResult? _invalidConfiguration;
    private readonly SemaphoreSlim _lifecycle = new(1, 1);
    private readonly CancellationTokenSource _disposed = new();
    private IManagedServerProcess? _process;
    private IServerJob? _job;
    private ServerProcessResult? _started;
    private Task _monitor = Task.CompletedTask;
    private int _generation;
    private int _disposeStarted;

    public ServerProcessManager(ServerProcessOptions options, ServerProcessDependencies dependencies)
    {
        _options = options;
        _dependencies = dependencies;
        var normalized = NormalizeBaseUrl(options.ApiUrl);
        EffectiveBaseUrl = normalized.BaseUrl ?? LocalBaseUrl(options.Port);
        if (normalized.Error != null)
        {
            _invalidConfiguration = new ServerProcessResult(ServerProcessState.Failed, EffectiveBaseUrl, false, ServerProcessError.InvalidApiUrl, normalized.Error);
        }
    }

    public Uri EffectiveBaseUrl { get; private set; }
    public bool OwnsProcess => _process != null;
    public bool IsAttached { get; private set; }

    public async Task<ServerProcessResult> EnsureStartedAsync(CancellationToken cancellationToken = default)
    {
        if (_disposeStarted != 0)
        {
            return new ServerProcessResult(ServerProcessState.Failed, EffectiveBaseUrl, false, ServerProcessError.Disposed);
        }

        if (_invalidConfiguration != null)
        {
            return _invalidConfiguration;
        }

        var remoteUrl = NormalizeBaseUrl(_options.ApiUrl).BaseUrl;
        if (remoteUrl != null)
        {
            EffectiveBaseUrl = remoteUrl;
            return new ServerProcessResult(ServerProcessState.Remote, EffectiveBaseUrl, false);
        }

        await _lifecycle.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            if (_started is { Error: ServerProcessError.RestartFailed })
            {
                return _started;
            }

            if (_started is { State: ServerProcessState.Started } && _process is { HasExited: false })
            {
                return _started;
            }

            if (await ProbeHealthyAsync(cancellationToken).ConfigureAwait(false))
            {
                IsAttached = true;
                _started = new ServerProcessResult(ServerProcessState.Attached, EffectiveBaseUrl, false);
                return _started;
            }

            return await StartOwnedAsync(cancellationToken).ConfigureAwait(false);
        }
        finally
        {
            _lifecycle.Release();
        }
    }

    public async Task RestartOwnedAsync(CancellationToken cancellationToken = default)
    {
        if (_disposeStarted != 0 || (!OwnsProcess && _started?.Error != ServerProcessError.RestartFailed))
        {
            return;
        }

        await _lifecycle.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            await DisposeOwnedProcessAsync(cancellationToken).ConfigureAwait(false);
            _started = null;
            await StartOwnedAsync(cancellationToken).ConfigureAwait(false);
        }
        finally
        {
            _lifecycle.Release();
        }
    }

    public async ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposeStarted, 1) != 0)
        {
            return;
        }

        _disposed.Cancel();
        await _lifecycle.WaitAsync().ConfigureAwait(false);
        try
        {
            await DisposeOwnedProcessAsync(CancellationToken.None).ConfigureAwait(false);
            _started = null;
        }
        finally
        {
            _lifecycle.Release();
            try { await _monitor.ConfigureAwait(false); } catch (OperationCanceledException) { }
            _disposed.Dispose();
        }
    }

    private async Task<ServerProcessResult> StartOwnedAsync(CancellationToken cancellationToken)
    {
        var binaryPath = BinaryPath();
        if (!_dependencies.VersionReader.Matches(binaryPath, _options.ExpectedVersion))
        {
            var acquired = await _dependencies.BinaryAcquirer
                .AcquireAsync(new ServerBinaryRequest(binaryPath, _options.ExpectedVersion), cancellationToken)
                .ConfigureAwait(false);
            if (!acquired || !_dependencies.VersionReader.Matches(binaryPath, _options.ExpectedVersion))
            {
                return Fail(ServerProcessError.BinaryUnavailable, binaryPath);
            }
        }

        IManagedServerProcess? process = null;
        IServerJob? job = null;
        try
        {
            process = _dependencies.Launcher.Start(new ServerLaunchRequest(binaryPath, _options.Port));
            job = _dependencies.JobAssigner.Assign(process);
        }
        catch (Exception ex)
        {
            if (process != null)
            {
                await CleanupFailedLaunchAsync(process, cancellationToken).ConfigureAwait(false);
            }
            var error = process == null ? ServerProcessError.LaunchFailed : ServerProcessError.JobAssignmentFailed;
            return Fail(error, ex.Message);
        }

        _process = process;
        _job = job;
        var generation = ++_generation;
        try
        {
            if (!await WaitUntilReadyAsync(cancellationToken).ConfigureAwait(false))
            {
                await DisposeOwnedProcessAsync(CancellationToken.None).ConfigureAwait(false);
                return Fail(ServerProcessError.ReadinessTimedOut, EffectiveBaseUrl.ToString());
            }
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            await DisposeOwnedProcessAsync(CancellationToken.None).ConfigureAwait(false);
            throw;
        }
        catch (Exception ex)
        {
            await DisposeOwnedProcessAsync(CancellationToken.None).ConfigureAwait(false);
            return Fail(ServerProcessError.ReadinessFailed, ex.Message);
        }

        IsAttached = false;
        process.Exited += OnOwnedProcessExited;
        _started = new ServerProcessResult(ServerProcessState.Started, EffectiveBaseUrl, true);
        return _started;
    }

    private async Task<bool> WaitUntilReadyAsync(CancellationToken cancellationToken)
    {
        for (var attempt = 0; attempt < _options.MaxReadinessAttempts; attempt++)
        {
            if (await ProbeHealthyAsync(cancellationToken).ConfigureAwait(false))
            {
                return true;
            }
            await _dependencies.Delay.DelayAsync(Backoff(attempt), cancellationToken).ConfigureAwait(false);
        }
        return false;
    }

    private Task<bool> ProbeHealthyAsync(CancellationToken cancellationToken) =>
        _dependencies.HealthProbe.IsHealthyAsync(EffectiveBaseUrl, _options.ExpectedVersion, cancellationToken);

    private ServerProcessResult Fail(ServerProcessError error, string detail)
    {
        _started = new ServerProcessResult(ServerProcessState.Failed, EffectiveBaseUrl, false, error, detail);
        return _started;
    }

    private async Task DisposeOwnedProcessAsync(CancellationToken cancellationToken)
    {
        var process = _process;
        _process = null;
        _generation++;
        _job?.Dispose();
        _job = null;
        if (process == null)
        {
            return;
        }
        try
        {
            process.Exited -= OnOwnedProcessExited;
            var exited = await process.WaitForExitAsync(TimeSpan.FromSeconds(2), cancellationToken).ConfigureAwait(false);
            if (!exited)
            {
                process.Kill();
                await process.WaitForExitAsync(TimeSpan.FromSeconds(2), CancellationToken.None).ConfigureAwait(false);
            }
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
        }
        process.Dispose();
    }

    private static async Task CleanupFailedLaunchAsync(IManagedServerProcess process, CancellationToken cancellationToken)
    {
        process.Kill();
        try
        {
            await process.WaitForExitAsync(TimeSpan.FromSeconds(2), cancellationToken).ConfigureAwait(false);
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
        }
        process.Dispose();
    }

}
