using System.Globalization;
using Amazon;
using Amazon.DynamoDBv2;
using Amazon.DynamoDBv2.Model;
using Amazon.Runtime;
using Amazon.SimpleEmailV2;
using Amazon.SimpleEmailV2.Model;
using Amazon.SQS;
using Amazon.SQS.Model;

namespace NotificationService;

// Thin adapters from the ports to AWS. Credentials come from the default
// chain: EKS Pod Identity in the cluster, environment variables locally.

public static class AwsConfig
{
    // A non-empty endpoint points the client at a local emulator.
    public static T For<T>(T config, string region, string? endpoint) where T : ClientConfig
    {
        if (string.IsNullOrWhiteSpace(endpoint))
        {
            config.RegionEndpoint = RegionEndpoint.GetBySystemName(region);
        }
        else
        {
            config.ServiceURL = endpoint;
            config.AuthenticationRegion = region;
        }
        return config;
    }
}

public sealed class SqsQueue(IAmazonSQS sqs, NotificationOptions options) : IQueue
{
    private const string ReceiveCountAttribute = "ApproximateReceiveCount";

    public async Task<IReadOnlyList<QueueMessage>> ReceiveAsync(CancellationToken ct)
    {
        var response = await sqs.ReceiveMessageAsync(new ReceiveMessageRequest
        {
            QueueUrl = options.QueueUrl,
            MaxNumberOfMessages = 10,
            WaitTimeSeconds = 20,
            MessageSystemAttributeNames = [ReceiveCountAttribute],
        }, ct);

        var messages = response.Messages ?? [];
        return messages
            .Select(m => new QueueMessage(
                m.MessageId,
                m.ReceiptHandle,
                m.Body ?? "",
                m.Attributes is not null && m.Attributes.TryGetValue(ReceiveCountAttribute, out var count)
                    && int.TryParse(count, NumberStyles.Integer, CultureInfo.InvariantCulture, out var n) ? n : 1))
            .ToList();
    }

    public Task DeleteAsync(QueueMessage message, CancellationToken ct) =>
        sqs.DeleteMessageAsync(new DeleteMessageRequest
        {
            QueueUrl = options.QueueUrl,
            ReceiptHandle = message.ReceiptHandle,
        }, ct);

    public Task ReleaseAsync(QueueMessage message, CancellationToken ct) =>
        sqs.ChangeMessageVisibilityAsync(new ChangeMessageVisibilityRequest
        {
            QueueUrl = options.QueueUrl,
            ReceiptHandle = message.ReceiptHandle,
            VisibilityTimeout = 0,
        }, ct);
}

public sealed class SesEmailSender(IAmazonSimpleEmailServiceV2 ses) : IEmailSender
{
    public Task SendAsync(EmailMessage email, CancellationToken ct) =>
        ses.SendEmailAsync(new SendEmailRequest
        {
            FromEmailAddress = email.From,
            Destination = new Destination { ToAddresses = [email.To] },
            Content = new EmailContent
            {
                Simple = new Amazon.SimpleEmailV2.Model.Message
                {
                    Subject = new Content { Data = email.Subject, Charset = "UTF-8" },
                    Body = new Body { Text = new Content { Data = email.Text, Charset = "UTF-8" } },
                },
            },
        }, ct);
}

// One item per event sent. Items expire after 7 days (DynamoDB TTL on
// "expires_at"), long after SQS could redeliver the same event.
public sealed class DynamoDedupeStore(IAmazonDynamoDB dynamo, NotificationOptions options, TimeProvider time) : IDedupeStore
{
    private static readonly TimeSpan Retention = TimeSpan.FromDays(7);

    public async Task<bool> WasSentAsync(string eventId, CancellationToken ct)
    {
        var response = await dynamo.GetItemAsync(new GetItemRequest
        {
            TableName = options.DedupeTable,
            Key = Key(eventId),
            ConsistentRead = true,
        }, ct);
        return response.Item is { Count: > 0 };
    }

    public Task MarkSentAsync(string eventId, CancellationToken ct)
    {
        var item = Key(eventId);
        item["expires_at"] = new AttributeValue
        {
            N = time.GetUtcNow().Add(Retention).ToUnixTimeSeconds().ToString(CultureInfo.InvariantCulture),
        };
        return dynamo.PutItemAsync(new PutItemRequest { TableName = options.DedupeTable, Item = item }, ct);
    }

    private static Dictionary<string, AttributeValue> Key(string eventId) =>
        new() { ["pk"] = new AttributeValue { S = "EVT#" + eventId } };
}