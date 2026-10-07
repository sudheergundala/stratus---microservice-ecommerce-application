using Amazon.DynamoDBv2;
using Amazon.SimpleEmailV2;
using Amazon.SQS;
using NotificationService;

var builder = WebApplication.CreateBuilder(args);
var config = builder.Configuration;

// JSON logs to stdout; quiet framework noise.
builder.Logging.ClearProviders();
builder.Logging.AddJsonConsole(o =>
{
    o.UseUtcTimestamp = true;
    o.TimestampFormat = "yyyy-MM-ddTHH:mm:ss.fffZ";
});
builder.Logging.AddFilter("Microsoft.AspNetCore", LogLevel.Warning);

// On SIGTERM: stop polling, finish the current message, release the rest.
builder.Services.Configure<HostOptions>(o =>
    o.ShutdownTimeout = TimeSpan.FromSeconds(config.GetValue("SHUTDOWN_TIMEOUT_SECONDS", 20)));

var options = new NotificationOptions(
    QueueUrl: config["SQS_QUEUE_URL"] ?? "",
    FromAddress: config["EMAIL_FROM"] ?? "",
    DedupeTable: config["DEDUPE_TABLE"] ?? "stratus-notification-dedupe");
var region = config["AWS_REGION"] ?? "us-east-1";

builder.Services.AddSingleton(options);
builder.Services.AddSingleton(TimeProvider.System);
builder.Services.AddSingleton<Heartbeat>();

// AWS clients. *_ENDPOINT variables are for local emulators only; unset in AWS.
builder.Services.AddSingleton<IAmazonSQS>(_ =>
    new AmazonSQSClient(AwsConfig.For(new AmazonSQSConfig(), region, config["SQS_ENDPOINT"])));
builder.Services.AddSingleton<IAmazonSimpleEmailServiceV2>(_ =>
    new AmazonSimpleEmailServiceV2Client(AwsConfig.For(new AmazonSimpleEmailServiceV2Config(), region, config["SES_ENDPOINT"])));
builder.Services.AddSingleton<IAmazonDynamoDB>(_ =>
    new AmazonDynamoDBClient(AwsConfig.For(new AmazonDynamoDBConfig(), region, config["DYNAMODB_ENDPOINT"])));

builder.Services.AddSingleton<IQueue, SqsQueue>();
builder.Services.AddSingleton<IEmailSender, SesEmailSender>();
builder.Services.AddSingleton<IDedupeStore, DynamoDedupeStore>();
builder.Services.AddSingleton<MessageProcessor>();
builder.Services.AddHostedService<NotificationWorker>();

await using var app = builder.Build();

// Fail fast on missing configuration.
if (string.IsNullOrWhiteSpace(options.QueueUrl) || string.IsNullOrWhiteSpace(options.FromAddress))
{
    app.Logger.LogCritical("SQS_QUEUE_URL and EMAIL_FROM are required");
    return 2;
}

// Probes. This service takes no user traffic; the port exists for Kubernetes.
var stallAfter = TimeSpan.FromSeconds(config.GetValue("LIVENESS_STALL_SECONDS", 90));
var draining = false;
app.Lifetime.ApplicationStopping.Register(() => draining = true);

app.MapGet("/healthz", (Heartbeat heartbeat) => Health.Liveness(heartbeat, stallAfter));
app.MapGet("/readyz", () => draining
    ? Results.Json(new { status = "draining" }, statusCode: StatusCodes.Status503ServiceUnavailable)
    : Results.Ok(new { status = "ready" }));

app.Logger.LogInformation("starting: queue {QueueUrl}, dedupe table {Table}", options.QueueUrl, options.DedupeTable);
await app.RunAsync();
return 0;