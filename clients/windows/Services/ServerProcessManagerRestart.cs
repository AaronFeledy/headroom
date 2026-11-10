namespace ClaudeUsageWidget.Services;

public sealed partial class ServerProcessManager
{
    private void OnOwnedProcessExited(object? sender, EventArgs args)
    {
        if (sender is not IManagedServerProcess process || _disposed.IsCancellationRequested)
        {
            return;
        }
        _monitor = RestartAfterExitAsync(process, _generation, _disposed.Token);
    }

    private async Task RestartAfterExitAsync(IManagedServerProcess exitedProcess, int generation, CancellationToken cancellationToken)
    {
        IManagedServerProcess? expectedProcess = exitedProcess;
        var expectedGeneration = generation;
        var expectedResult = _started;
        try
        {
            for (var attempt = 0; attempt < _options.MaxRestartAttempts; attempt++)
            {
                await _dependencies.Delay.DelayAsync(Backoff(attempt), cancellationToken).ConfigureAwait(false);
                await _lifecycle.WaitAsync(cancellationToken).ConfigureAwait(false);
                try
                {
                    if (_disposeStarted != 0 || cancellationToken.IsCancellationRequested
                        || !ReferenceEquals(_process, expectedProcess) || expectedGeneration != _generation
                        || !ReferenceEquals(_started, expectedResult))
                    {
                        return;
                    }
                    try
                    {
                        await DisposeOwnedProcessAsync(cancellationToken).ConfigureAwait(false);
                        cancellationToken.ThrowIfCancellationRequested();
                        _started = null;
                        var result = await StartOwnedAsync(cancellationToken).ConfigureAwait(false);
                        if (result.State == ServerProcessState.Started)
                        {
                            return;
                        }
                    }
                    catch (Exception ex) when (!cancellationToken.IsCancellationRequested)
                    {
                        Fail(ServerProcessError.LaunchFailed, ex.Message);
                    }
                    expectedProcess = _process;
                    expectedGeneration = _generation;
                    expectedResult = _started;
                    if (attempt == _options.MaxRestartAttempts - 1)
                    {
                        SetRestartFailed();
                    }
                }
                finally
                {
                    _lifecycle.Release();
                }
            }
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
        }
    }

    private void SetRestartFailed()
    {
        _started = new ServerProcessResult(ServerProcessState.Failed, EffectiveBaseUrl, false, ServerProcessError.RestartFailed, "Automatic local server restart failed.");
    }

}
