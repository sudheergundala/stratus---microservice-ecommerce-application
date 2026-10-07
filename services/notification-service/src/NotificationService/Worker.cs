namespace NotificationService;

// Proof of life for the liveness probe: the poll loop records each turn.
// A loop that stops turning (deadlock, hung call) makes /healthz fail,
// and Kubernetes restarts the pod.
public sealed class Heartbeat(TimeProvider time)
{
    private long _lastTicks = time.GetUtcNow().UtcTicks;

    public void Beat() => Interlocked.Exchange(ref _lastTicks, time.GetUtcNow().UtcTicks);

    public TimeSpan Age => time.GetUtcNow() - new DateTimeOffset(Interlocked.Read(ref _lastTicks), TimeSpan.Zero);
}

public static class Health
{
    public static IResult Liveness(Heartbeat heartbeat, TimeSpan stallAfter)
    {
        var seconds = (int)heartbeat.Age.TotalSeconds;
        return heartbeat.Age > stallAfter
            ? Results.Json(new { status = "stalled", seconds_since_last_poll = seconds }, statusCode: StatusCodes.Status503ServiceUnavailable)
            : Results.Ok(new { status = "ok", seconds_since_last_poll = seconds });
    }
}

public sealed class NotificationWorker(
    IQueue queue, MessageProcessor processor, Heartbeat heartbeat, ILogger<NotificationWorker> log) : BackgroundService
{
    private static readonly TimeSpan MessageTimeout = TimeSpan.FromSeconds(15); // below the 20 s shutdown timeout

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        log.LogInformation("worker started");
        var consecutiveFailures = 0;

        while (!stoppingToken.IsCancellationRequested)
        {
            heartbeat.Beat();

            IReadOnlyList<QueueMessage> batch;
            try
            {
                // Long poll: waits up to 20 s for messages. Cancelled instantly on SIGTERM.
                batch = await queue.ReceiveAsync(stoppingToken);
                consecutiveFailures = 0;
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                break;
            }
            catch (Exception ex)
            {
                // Queue unreachable: back off (2, 4, 8 … 30 s) instead of hammering it.
                // The heartbeat keeps beating, so liveness stays healthy: restarting
                // the pod would not fix an SQS outage.
                consecutiveFailures++;
                var delay = TimeSpan.FromSeconds(Math.Min(30, Math.Pow(2, consecutiveFailures)));
                log.LogError(ex, "receive failed; retrying in {DelaySeconds} s", delay.TotalSeconds);
                try
                {
                    await Task.Delay(delay, stoppingToken);
                }
                catch (OperationCanceledException)
                {
                    break;
                }
                continue;
            }

            for (var i = 0; i < batch.Count; i++)
            {
                if (stoppingToken.IsCancellationRequested)
                {
                    // Shutting down: hand unprocessed messages back right away
                    // instead of making them wait out the visibility timeout.
                    await ReleaseAsync(batch.Skip(i));
                    break;
                }
                heartbeat.Beat();
                await HandleAsync(batch[i]);
            }
        }

        log.LogInformation("worker stopped");
    }

    // The message being processed when SIGTERM arrives is finished, not abandoned:
    // it uses its own timeout, not the shutdown token.
    private async Task HandleAsync(QueueMessage message)
    {
        using var timeout = new CancellationTokenSource(MessageTimeout);
        var outcome = await processor.ProcessAsync(message, timeout.Token);
        if (!MessageProcessor.ShouldDelete(outcome))
        {
            return;
        }
        try
        {
            await queue.DeleteAsync(message, timeout.Token);
        }
        catch (Exception ex)
        {
            // Not fatal: the message reappears and the dedupe store skips it.
            log.LogWarning(ex, "delete failed for {MessageId}; it will be redelivered", message.MessageId);
        }
    }

    private async Task ReleaseAsync(IEnumerable<QueueMessage> messages)
    {
        foreach (var message in messages)
        {
            try
            {
                using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(5));
                await queue.ReleaseAsync(message, timeout.Token);
            }
            catch (Exception ex)
            {
                log.LogWarning(ex, "release failed for {MessageId}; it returns after the visibility timeout", message.MessageId);
            }
        }
    }
}