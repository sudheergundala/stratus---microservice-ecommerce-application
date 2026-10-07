using System.Text.Json;
using Microsoft.AspNetCore.Http;
using Microsoft.Extensions.Logging.Abstractions;
using NotificationService;
using Xunit;

namespace NotificationService.Tests;

// ---- Fakes ----------------------------------------------------------------

public sealed class FakeSender : IEmailSender
{
    public List<EmailMessage> Sent { get; } = [];
    public Exception? Fail { get; set; }

    public Task SendAsync(EmailMessage email, CancellationToken ct)
    {
        if (Fail is not null) throw Fail;
        Sent.Add(email);
        return Task.CompletedTask;
    }
}

public sealed class FakeDedupe : IDedupeStore
{
    public HashSet<string> Ids { get; } = [];

    public Task<bool> WasSentAsync(string eventId, CancellationToken ct) => Task.FromResult(Ids.Contains(eventId));

    public Task MarkSentAsync(string eventId, CancellationToken ct)
    {
        Ids.Add(eventId);
        return Task.CompletedTask;
    }
}

// Hands out the given batches, then behaves like an empty long poll.
public sealed class FakeQueue(params IReadOnlyList<QueueMessage>[] batches) : IQueue
{
    private readonly Queue<IReadOnlyList<QueueMessage>> _batches = new(batches);

    public List<string> Deleted { get; } = [];
    public TaskCompletionSource Drained { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);

    public async Task<IReadOnlyList<QueueMessage>> ReceiveAsync(CancellationToken ct)
    {
        if (_batches.Count > 0) return _batches.Dequeue();
        Drained.TrySetResult();
        await Task.Delay(Timeout.Infinite, ct);
        return [];
    }

    public Task DeleteAsync(QueueMessage message, CancellationToken ct)
    {
        Deleted.Add(message.MessageId);
        return Task.CompletedTask;
    }

    public Task ReleaseAsync(QueueMessage message, CancellationToken ct) => Task.CompletedTask;
}

public sealed class ManualTime(DateTimeOffset start) : TimeProvider
{
    public DateTimeOffset Now { get; set; } = start;

    public override DateTimeOffset GetUtcNow() => Now;
}

// ---- Tests ----------------------------------------------------------------

public sealed class NotificationTests
{
    private readonly FakeSender _sender = new();
    private readonly FakeDedupe _dedupe = new();
    private readonly MessageProcessor _processor;

    public NotificationTests()
    {
        _processor = new MessageProcessor(_sender, _dedupe,
            new NotificationOptions("queue-url", "orders@stratus.example", "dedupe-table"),
            NullLogger<MessageProcessor>.Instance);
    }

    private static string Event(string id = "evt-1", string type = "OrderPlaced", string email = "ada@example.com") =>
        JsonSerializer.Serialize(new
        {
            event_id = id,
            event_type = type,
            order_id = "ord_42",
            customer_email = email,
            total_cents = 4599,
            currency = "USD",
            reason = "payment declined",
        });

    private static QueueMessage Msg(string body, string id = "m-1") => new(id, "receipt-" + id, body, 1);

    private Task<Outcome> Process(string body) => _processor.ProcessAsync(Msg(body), CancellationToken.None);

    [Fact]
    public async Task Order_placed_sends_one_confirmation_email()
    {
        Assert.Equal(Outcome.Sent, await Process(Event()));

        var email = Assert.Single(_sender.Sent);
        Assert.Equal("ada@example.com", email.To);
        Assert.Equal("orders@stratus.example", email.From);
        Assert.Contains("ord_42", email.Subject);
        Assert.Contains("45.99 USD", email.Text);
        Assert.Contains("evt-1", _dedupe.Ids);
    }

    [Fact]
    public async Task Order_failed_email_includes_the_reason()
    {
        await Process(Event(type: "OrderFailed"));
        Assert.Contains("payment declined", Assert.Single(_sender.Sent).Text);
    }

    [Fact]
    public async Task Redelivered_event_is_not_emailed_twice()
    {
        Assert.Equal(Outcome.Sent, await Process(Event()));
        Assert.Equal(Outcome.Duplicate, await Process(Event()));
        Assert.Single(_sender.Sent);
    }

    [Fact]
    public async Task Sns_envelope_is_unwrapped()
    {
        var envelope = JsonSerializer.Serialize(new { Type = "Notification", MessageId = "sns-1", Message = Event() });
        Assert.Equal(Outcome.Sent, await Process(envelope));
    }

    [Theory]
    [InlineData("not json")]
    [InlineData("""{"event_type":"OrderPlaced","order_id":"o","customer_email":"a@b.com","currency":"USD"}""")]
    [InlineData("""{"event_id":"e","event_type":"OrderPlaced","order_id":"o","customer_email":"not-an-email","currency":"USD"}""")]
    public async Task Invalid_messages_are_kept_for_the_dead_letter_queue(string body)
    {
        var outcome = await Process(body);
        Assert.Equal(Outcome.Invalid, outcome);
        Assert.False(MessageProcessor.ShouldDelete(outcome));
        Assert.Empty(_sender.Sent);
    }

    [Fact]
    public async Task Unknown_event_types_are_ignored_and_deleted()
    {
        var outcome = await Process(Event(type: "OrderShipped"));
        Assert.Equal(Outcome.Ignored, outcome);
        Assert.True(MessageProcessor.ShouldDelete(outcome));
        Assert.Empty(_sender.Sent);
    }

    [Fact]
    public async Task Email_failure_keeps_the_message_for_retry()
    {
        _sender.Fail = new HttpRequestException("SES unavailable");
        var outcome = await Process(Event());

        Assert.Equal(Outcome.Failed, outcome);
        Assert.False(MessageProcessor.ShouldDelete(outcome));
        Assert.Empty(_dedupe.Ids); // not marked: the retry must send it
    }

    [Fact]
    public async Task Worker_deletes_handled_messages_and_keeps_failed_ones()
    {
        var queue = new FakeQueue(new List<QueueMessage> { Msg(Event(), "good"), Msg("not json", "bad") });
        var worker = new NotificationWorker(queue, _processor, new Heartbeat(TimeProvider.System),
            NullLogger<NotificationWorker>.Instance);

        await worker.StartAsync(CancellationToken.None);
        await queue.Drained.Task.WaitAsync(TimeSpan.FromSeconds(5));
        await worker.StopAsync(CancellationToken.None);

        Assert.Equal(new[] { "good" }, queue.Deleted);
        Assert.Single(_sender.Sent);
    }

    [Fact]
    public void Liveness_fails_when_the_poll_loop_stalls()
    {
        var time = new ManualTime(DateTimeOffset.UnixEpoch);
        var heartbeat = new Heartbeat(time);
        var stallAfter = TimeSpan.FromSeconds(90);

        Assert.Equal(200, Status(Health.Liveness(heartbeat, stallAfter)));

        time.Now += TimeSpan.FromSeconds(91);
        Assert.Equal(503, Status(Health.Liveness(heartbeat, stallAfter)));

        heartbeat.Beat();
        Assert.Equal(200, Status(Health.Liveness(heartbeat, stallAfter)));
    }

    private static int? Status(IResult result) => ((IStatusCodeHttpResult)result).StatusCode;
}