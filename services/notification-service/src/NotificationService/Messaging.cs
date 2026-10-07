using System.Globalization;
using System.Net.Mail;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace NotificationService;

// ---- Contract: the events this service consumes --------------------------

// Published by order-service through SNS -> SQS. One event = at most one email.
public sealed record OrderEvent(
    string EventId,
    string EventType,
    string OrderId,
    string CustomerEmail,
    long TotalCents,
    string Currency,
    string? Reason)
{
    private static readonly JsonSerializerOptions Json = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
        PropertyNameCaseInsensitive = true,
    };

    // Accepts the raw event, or the same event wrapped in an SNS envelope
    // (if raw message delivery is ever turned off on the subscription).
    public static bool TryParse(string body, out OrderEvent evt, out string error)
    {
        evt = null!;
        try
        {
            using var doc = JsonDocument.Parse(body);
            var root = doc.RootElement;
            if (root.ValueKind == JsonValueKind.Object
                && root.TryGetProperty("Type", out var type) && type.ValueKind == JsonValueKind.String && type.GetString() == "Notification"
                && root.TryGetProperty("Message", out var inner) && inner.ValueKind == JsonValueKind.String)
            {
                return TryParse(inner.GetString()!, out evt, out error);
            }

            var parsed = root.Deserialize<OrderEvent>(Json);
            if (parsed is null || string.IsNullOrWhiteSpace(parsed.EventId) || string.IsNullOrWhiteSpace(parsed.EventType)
                || string.IsNullOrWhiteSpace(parsed.OrderId) || string.IsNullOrWhiteSpace(parsed.Currency))
            {
                error = "missing required fields (event_id, event_type, order_id, currency)";
                return false;
            }
            if (!MailAddress.TryCreate(parsed.CustomerEmail, out _))
            {
                error = "customer_email is not a valid address";
                return false;
            }
            evt = parsed;
            error = "";
            return true;
        }
        catch (JsonException ex)
        {
            error = "body is not valid JSON: " + ex.Message;
            return false;
        }
    }
}

// ---- Email content -------------------------------------------------------

public sealed record EmailMessage(string From, string To, string Subject, string Text);

public static class EmailTemplates
{
    // Returns false for event types this service does not email about.
    public static bool TryRender(OrderEvent evt, string from, out EmailMessage email)
    {
        var total = (evt.TotalCents / 100m).ToString("0.00", CultureInfo.InvariantCulture) + " " + evt.Currency;
        (string Subject, string Text)? content = evt.EventType switch
        {
            "OrderPlaced" => (
                $"Your Stratus order {evt.OrderId} is confirmed",
                $"Thanks for your order!\n\nOrder: {evt.OrderId}\nTotal: {total}\n\nWe'll let you know when it ships.\n\nStratus"),
            "OrderFailed" => (
                $"We couldn't complete your Stratus order {evt.OrderId}",
                $"Sorry, we couldn't complete your order.\n\nOrder: {evt.OrderId}\nReason: {evt.Reason ?? "unknown"}\n\nYou have not been charged.\n\nStratus"),
            _ => null,
        };
        if (content is null)
        {
            email = null!;
            return false;
        }
        email = new EmailMessage(from, evt.CustomerEmail, content.Value.Subject, content.Value.Text);
        return true;
    }
}

// ---- Ports: what the processor needs from the outside world --------------

public sealed record QueueMessage(string MessageId, string ReceiptHandle, string Body, int ReceiveCount);

public interface IQueue
{
    Task<IReadOnlyList<QueueMessage>> ReceiveAsync(CancellationToken ct);

    // Done with the message: remove it from the queue.
    Task DeleteAsync(QueueMessage message, CancellationToken ct);

    // Not processed (shutting down): make it visible to other workers now.
    Task ReleaseAsync(QueueMessage message, CancellationToken ct);
}

public interface IEmailSender
{
    Task SendAsync(EmailMessage email, CancellationToken ct);
}

public interface IDedupeStore
{
    Task<bool> WasSentAsync(string eventId, CancellationToken ct);

    Task MarkSentAsync(string eventId, CancellationToken ct);
}

public sealed record NotificationOptions(string QueueUrl, string FromAddress, string DedupeTable);

// ---- Processing ----------------------------------------------------------

public enum Outcome
{
    Sent,       // email sent             -> delete message
    Duplicate,  // already sent before    -> delete message
    Ignored,    // not an event we email  -> delete message
    Invalid,    // can never succeed      -> keep; goes to the DLQ after maxReceiveCount
    Failed,     // may succeed later      -> keep; retried after the visibility timeout
}

public sealed class MessageProcessor(IEmailSender sender, IDedupeStore dedupe, NotificationOptions options, ILogger<MessageProcessor> log)
{
    public static bool ShouldDelete(Outcome outcome) => outcome is Outcome.Sent or Outcome.Duplicate or Outcome.Ignored;

    public async Task<Outcome> ProcessAsync(QueueMessage message, CancellationToken ct)
    {
        if (!OrderEvent.TryParse(message.Body, out var evt, out var error))
        {
            log.LogWarning("invalid message {MessageId} (receive {ReceiveCount}): {Error}", message.MessageId, message.ReceiveCount, error);
            return Outcome.Invalid;
        }
        if (!EmailTemplates.TryRender(evt, options.FromAddress, out var email))
        {
            log.LogInformation("ignored event type {EventType} {EventId}", evt.EventType, evt.EventId);
            return Outcome.Ignored;
        }

        try
        {
            // SQS delivers at least once: the same event can arrive twice.
            if (await dedupe.WasSentAsync(evt.EventId, ct))
            {
                log.LogInformation("duplicate event skipped {EventId} {OrderId}", evt.EventId, evt.OrderId);
                return Outcome.Duplicate;
            }
            await sender.SendAsync(email, ct);
            await dedupe.MarkSentAsync(evt.EventId, ct);
            // Log ids only: the customer's email address is personal data.
            log.LogInformation("email sent {EventType} {EventId} {OrderId}", evt.EventType, evt.EventId, evt.OrderId);
            return Outcome.Sent;
        }
        catch (Exception ex)
        {
            // Includes the per-message timeout. The message stays in the queue.
            log.LogError(ex, "processing failed {EventId} (receive {ReceiveCount}); will retry", evt.EventId, message.ReceiveCount);
            return Outcome.Failed;
        }
    }
}