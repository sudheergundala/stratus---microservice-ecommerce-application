using System.Text.Json.Serialization;
using Microsoft.AspNetCore.Diagnostics;
using Microsoft.AspNetCore.Http.Timeouts;
using Microsoft.AspNetCore.Identity;
using Microsoft.EntityFrameworkCore;
using Npgsql;
using UserService.Api;
using UserService.Auth;
using UserService.Data;

// Two modes, one image:
//   dotnet UserService.dll           -> HTTP server
//   dotnet UserService.dll migrate   -> apply database migrations, then exit (Kubernetes Job)
var migrateOnly = args.Contains("migrate");

var builder = WebApplication.CreateBuilder(args);

// JSON logs to stdout. Quiet framework noise; keep startup and shutdown messages.
builder.Logging.ClearProviders();
builder.Logging.AddJsonConsole(o =>
{
    o.UseUtcTimestamp = true;
    o.TimestampFormat = "yyyy-MM-ddTHH:mm:ss.fffZ";
});
builder.Logging.AddFilter("Microsoft.AspNetCore", LogLevel.Warning);
builder.Logging.AddFilter("Microsoft.EntityFrameworkCore", LogLevel.Warning);

// Graceful shutdown: on SIGTERM, finish in-flight requests for up to this long.
builder.Services.Configure<HostOptions>(o =>
    o.ShutdownTimeout = TimeSpan.FromSeconds(builder.Configuration.GetValue("SHUTDOWN_TIMEOUT_SECONDS", 20)));

// Request limits: 1 MiB bodies, unknown JSON fields rejected (no mass assignment),
// and every request bounded in time (including its database work).
builder.WebHost.ConfigureKestrel(k => k.Limits.MaxRequestBodySize = 1024 * 1024);
builder.Services.ConfigureHttpJsonOptions(o =>
    o.SerializerOptions.UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow);
builder.Services.AddRequestTimeouts(o => o.DefaultPolicy = new RequestTimeoutPolicy
{
    Timeout = TimeSpan.FromSeconds(builder.Configuration.GetValue("REQUEST_TIMEOUT_SECONDS", 5)),
    TimeoutStatusCode = StatusCodes.Status503ServiceUnavailable,
});

// PostgreSQL. The connection string comes from ConnectionStrings__UsersDb.
builder.Services.AddDbContextPool<UserDbContext>((sp, o) =>
{
    var config = sp.GetRequiredService<IConfiguration>();
    o.UseNpgsql(config.GetConnectionString("UsersDb"), npgsql =>
    {
        npgsql.CommandTimeout(config.GetValue("DB_COMMAND_TIMEOUT_SECONDS", 5));
        npgsql.EnableRetryOnFailure(maxRetryCount: 3);
    });
});
builder.Services.AddScoped<IUserStore, EfUserStore>();

// Passwords: PBKDF2 (ASP.NET Core Identity's hasher). Tokens: RS256 JWT.
builder.Services.AddSingleton<IPasswordHasher<User>, PasswordHasher<User>>();
builder.Services.AddSingleton(TimeProvider.System);
builder.Services.AddSingleton(sp =>
{
    var config = sp.GetRequiredService<IConfiguration>();
    return new JwtSettings(
        PrivateKeyPem: config["JWT_PRIVATE_KEY"] ?? "",
        Issuer: config["JWT_ISSUER"] ?? "stratus-user-service",
        Audience: config["JWT_AUDIENCE"] ?? "stratus-api",
        Lifetime: TimeSpan.FromMinutes(config.GetValue("JWT_LIFETIME_MINUTES", 15)));
});
builder.Services.AddSingleton(sp =>
    new TokenIssuer(sp.GetRequiredService<JwtSettings>(), sp.GetRequiredService<TimeProvider>()));

await using var app = builder.Build();

// Fail fast on missing configuration instead of failing on the first request.
if (string.IsNullOrWhiteSpace(app.Configuration.GetConnectionString("UsersDb")))
{
    app.Logger.LogCritical("ConnectionStrings__UsersDb is required");
    return 2;
}

if (migrateOnly)
{
    using var scope = app.Services.CreateScope();
    var db = scope.ServiceProvider.GetRequiredService<UserDbContext>();
    app.Logger.LogInformation("applying database migrations");
    await db.Database.MigrateAsync();
    app.Logger.LogInformation("database migrations complete");
    return 0;
}

try
{
    app.Services.GetRequiredService<TokenIssuer>();
}
catch (InvalidOperationException ex)
{
    app.Logger.LogCritical("invalid JWT configuration: {Reason}", ex.Message);
    return 2;
}

// Errors: database problems -> 503 (retry later); anything else -> 500.
// Details are logged, never returned to the caller.
app.UseExceptionHandler(errorApp => errorApp.Run(async context =>
{
    var error = context.Features.Get<IExceptionHandlerFeature>()?.Error;
    var (status, message) = error switch
    {
        BadHttpRequestException bad => (bad.StatusCode, "invalid request"),
        _ when error?.GetBaseException() is NpgsqlException or TimeoutException => (503, "user store unavailable; retry"),
        _ => (500, "internal error"),
    };
    context.Response.StatusCode = status;
    await context.Response.WriteAsJsonAsync(new { error = message });
}));
app.UseRequestTimeouts();

// Readiness fails once shutdown starts, so load balancers stop sending traffic.
var draining = false;
app.Lifetime.ApplicationStopping.Register(() => draining = true);

// Liveness never checks PostgreSQL: a database outage must not restart every pod.
app.MapGet("/healthz", () => Results.Ok(new { status = "ok" }));
app.MapGet("/readyz", () => draining
    ? Results.Json(new { status = "draining" }, statusCode: StatusCodes.Status503ServiceUnavailable)
    : Results.Ok(new { status = "ready" }));
app.MapAuth();

await app.RunAsync();
return 0;

// Lets the integration tests start the app in memory.
public partial class Program { }