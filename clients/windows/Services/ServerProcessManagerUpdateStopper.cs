namespace ClaudeUsageWidget.Services;

public sealed partial class ServerProcessManager : IPendingUpdateServerStopper
{
    public async Task StopOwnedServerAsync(CancellationToken cancellationToken)
    {
        if (_disposeStarted != 0)
        {
            return;
        }

        await _lifecycle.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            await DisposeOwnedProcessAsync(cancellationToken).ConfigureAwait(false);
            _started = null;
        }
        finally
        {
            _lifecycle.Release();
        }
    }
}
