using ClaudeUsageWidget.Services;
using System.Net;

internal sealed class FakeDependencies
{
    public FakeDependencies()
    {
        Version = new FakeVersionReader(Order);
        Acquirer = new FakeAcquirer(Order);
        Launcher = new FakeLauncher(Order);
        Job = new FakeJobAssigner(Order);
        Value = new ServerProcessDependencies(Health, Version, Acquirer, Launcher, Job, Delay);
    }

    public FakeHealthProbe Health { get; } = new();
    public FakeVersionReader Version { get; }
    public FakeAcquirer Acquirer { get; }
    public FakeLauncher Launcher { get; }
    public FakeJobAssigner Job { get; }
    public FakeDelay Delay { get; } = new();
    public List<string> Order { get; } = new();
    public ServerProcessDependencies Value { get; }

    public static FakeDependencies Create() => new();

    public static FakeDependencies CreateReadyToLaunch()
    {
        var deps = new FakeDependencies();
        deps.Health.Results.Enqueue(false);
        deps.Health.Results.Enqueue(true);
        deps.Version.MatchesQueue.Enqueue(true);
        return deps;
    }
}

internal sealed class FakeHealthProbe : IServerHealthProbe
{
    public Queue<bool> Results { get; } = new();
    public int Calls { get; private set; }
    public int ThrowAfterCalls { get; set; }
    public int ThrowCancellationAfterCalls { get; set; }
    public CancellationTokenSource? CancelWhenThrowing { get; set; }
    public TaskCompletionSource ThrowObserved { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);

    public Task<bool> IsHealthyAsync(Uri baseUrl, string expectedVersion, CancellationToken cancellationToken)
    {
        Calls++;
        if (ThrowCancellationAfterCalls == Calls)
        {
            CancelWhenThrowing?.Cancel();
            throw new OperationCanceledException(cancellationToken);
        }
        if (ThrowAfterCalls == Calls)
        {
            ThrowObserved.TrySetResult();
            throw new InvalidOperationException("health failed");
        }
        return Task.FromResult(Results.Count > 0 && Results.Dequeue());
    }
}

internal sealed class FakeVersionReader : IServerBinaryVersionReader
{
    private readonly List<string> _order;
    public Queue<bool> MatchesQueue { get; } = new();

    public FakeVersionReader(List<string> order)
    {
        _order = order;
    }

    public bool Matches(string path, string expectedVersion)
    {
        _order.Add("version");
        return MatchesQueue.Count == 0 || MatchesQueue.Dequeue();
    }
}

internal sealed class FakeAcquirer : IServerBinaryAcquirer
{
    private readonly List<string> _order;
    public int Calls { get; private set; }
    public bool Result { get; set; }
    public bool ThrowCancellation { get; set; }
    public bool ThrowException { get; set; }
    public TaskCompletionSource CalledAgain { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);

    public FakeAcquirer(List<string> order)
    {
        _order = order;
    }

    public Task<bool> AcquireAsync(ServerBinaryRequest request, CancellationToken cancellationToken)
    {
        _order.Add("acquire");
        Calls++;
        CalledAgain.TrySetResult();
        if (ThrowCancellation)
        {
            throw new OperationCanceledException(cancellationToken);
        }
        if (ThrowException)
        {
            throw new InvalidOperationException("acquisition failed");
        }
        return Task.FromResult(Result);
    }
}

internal sealed class FakeLauncher : IServerProcessLauncher
{
    private readonly List<string> _order;
    public int Starts { get; private set; }
    public FakeProcess? LastProcess { get; private set; }
    public TaskCompletionSource? SecondStarted { get; set; }

    public FakeLauncher(List<string> order)
    {
        _order = order;
    }

    public IManagedServerProcess Start(ServerLaunchRequest request)
    {
        _order.Add("launch");
        Starts++;
        LastProcess = new FakeProcess(Starts);
        if (Starts == 2)
        {
            SecondStarted?.TrySetResult();
        }
        return LastProcess;
    }
}

internal sealed class FakeProcess : IManagedServerProcess
{
    public FakeProcess(int id)
    {
        Id = id;
    }

    public int Id { get; }
    public bool HasExited { get; private set; }
    public IntPtr Handle => IntPtr.Zero;
    public bool Disposed { get; private set; }
    public bool Killed { get; private set; }
    public event EventHandler? Exited;

    public Task<bool> WaitForExitAsync(TimeSpan timeout, CancellationToken cancellationToken) => Task.FromResult(HasExited);
    public void Kill()
    {
        Killed = true;
        HasExited = true;
    }
    public void Dispose() => Disposed = true;

    public void RaiseExited()
    {
        HasExited = true;
        Exited?.Invoke(this, EventArgs.Empty);
    }
}

internal sealed class FakeJobAssigner : IServerJobAssigner
{
    private readonly List<string> _order;
    public int Assigns { get; private set; }
    public FakeJob? LastJob { get; private set; }
    public bool ThrowOnAssign { get; set; }

    public FakeJobAssigner(List<string> order)
    {
        _order = order;
    }

    public IServerJob Assign(IManagedServerProcess process)
    {
        _order.Add("job");
        if (ThrowOnAssign)
        {
            throw new InvalidOperationException("job assignment failed");
        }
        Assigns++;
        LastJob = new FakeJob();
        return LastJob;
    }
}

internal sealed class FakeJob : IServerJob
{
    public bool Disposed { get; private set; }
    public void Dispose() => Disposed = true;
}

internal sealed class FakeDelay : IServerDelay
{
    public List<TimeSpan> Delays { get; } = new();
    public Func<int, Task>? OnDelay { get; set; }

    public Task DelayAsync(TimeSpan delay, CancellationToken cancellationToken)
    {
        cancellationToken.ThrowIfCancellationRequested();
        Delays.Add(delay);
        return (OnDelay?.Invoke(Delays.Count) ?? Task.CompletedTask).WaitAsync(cancellationToken);
    }
}

internal sealed class FakeReleaseHandler : HttpMessageHandler
{
    private readonly bool _downloadSucceeds;

    public FakeReleaseHandler(bool downloadSucceeds)
    {
        _downloadSucceeds = downloadSucceeds;
    }

    protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        if (request.RequestUri?.AbsoluteUri.Contains("releases/latest", StringComparison.OrdinalIgnoreCase) == true)
        {
            const string json = "{\"assets\":[{\"name\":\"usage-server-win-x64.exe\",\"browser_download_url\":\"https://example.test/usage-server.exe\"}]}";
            return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK) { Content = new StringContent(json) });
        }

        if (!_downloadSucceeds)
        {
            return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK) { Content = new ByteArrayContent(new byte[8]) });
        }

        return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK) { Content = new ByteArrayContent([1, 2, 3, 4]) });
    }
}

internal sealed class FakeHealthHandler : HttpMessageHandler
{
    private readonly string _json;

    public FakeHealthHandler(string json)
    {
        _json = json;
    }

    protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK) { Content = new StringContent(_json) });
    }
}
