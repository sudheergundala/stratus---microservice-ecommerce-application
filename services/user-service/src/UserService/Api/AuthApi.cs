using System.Net.Mail;
using Microsoft.AspNetCore.Identity;
using UserService.Auth;
using UserService.Data;

namespace UserService.Api;

public sealed record Credentials(string? Email, string? Password);

public static class AuthApi
{
    private const int MinPasswordLength = 12;
    private const int MaxPasswordLength = 128;

    // Checked when an email is unknown, so the response time does not reveal
    // which emails are registered.
    private static readonly User DummyUser = new();
    private static readonly string DummyHash = new PasswordHasher<User>().HashPassword(DummyUser, "timing-equalizer-not-a-real-password");

    public static void MapAuth(this IEndpointRouteBuilder app)
    {
        app.MapPost("/auth/register", Register);
        app.MapPost("/auth/login", Login);
        app.MapGet("/.well-known/jwks.json", Jwks);
    }

    private static async Task<IResult> Register(
        Credentials body, IUserStore store, IPasswordHasher<User> hasher, TimeProvider time,
        ILoggerFactory logs, CancellationToken ct)
    {
        var email = Normalize(body.Email);
        if (!IsValidEmail(email))
        {
            return Results.UnprocessableEntity(new { error = "a valid email is required" });
        }
        if (body.Password is null || body.Password.Length < MinPasswordLength || body.Password.Length > MaxPasswordLength)
        {
            return Results.UnprocessableEntity(new { error = $"password must be {MinPasswordLength} to {MaxPasswordLength} characters" });
        }

        var user = new User { Id = Guid.CreateVersion7(), Email = email, CreatedAt = time.GetUtcNow() };
        user.PasswordHash = hasher.HashPassword(user, body.Password);

        if (!await store.TryAddAsync(user, ct))
        {
            return Results.Conflict(new { error = "email already registered" });
        }

        // Log the id, never the email or password.
        logs.CreateLogger("UserService.Auth").LogInformation("user registered {UserId}", user.Id);
        return Results.Json(new { id = user.Id, email = user.Email }, statusCode: StatusCodes.Status201Created);
    }

    private static async Task<IResult> Login(
        Credentials body, IUserStore store, IPasswordHasher<User> hasher, TokenIssuer issuer,
        ILoggerFactory logs, CancellationToken ct)
    {
        var email = Normalize(body.Email);
        if (email.Length == 0 || string.IsNullOrEmpty(body.Password))
        {
            return Results.UnprocessableEntity(new { error = "email and password are required" });
        }

        var user = await store.FindByEmailAsync(email, ct);
        if (user is null)
        {
            hasher.VerifyHashedPassword(DummyUser, DummyHash, body.Password);
            return InvalidCredentials();
        }
        if (hasher.VerifyHashedPassword(user, user.PasswordHash, body.Password) == PasswordVerificationResult.Failed)
        {
            logs.CreateLogger("UserService.Auth").LogInformation("login failed {UserId}", user.Id);
            return InvalidCredentials();
        }

        var (token, expiresIn) = issuer.Issue(user);
        logs.CreateLogger("UserService.Auth").LogInformation("login succeeded {UserId}", user.Id);
        return Results.Ok(new { access_token = token, token_type = "Bearer", expires_in = expiresIn });
    }

    private static IResult Jwks(TokenIssuer issuer, HttpContext http)
    {
        // The gateway caches this; keys change only on rotation.
        http.Response.Headers.CacheControl = "public, max-age=300";
        return Results.Json(issuer.Jwks);
    }

    // Same message for unknown email and wrong password: no account enumeration at login.
    private static IResult InvalidCredentials() =>
        Results.Json(new { error = "invalid email or password" }, statusCode: StatusCodes.Status401Unauthorized);

    private static string Normalize(string? email) => (email ?? "").Trim().ToLowerInvariant();

    private static bool IsValidEmail(string email) =>
        email.Length is >= 3 and <= 254
        && MailAddress.TryCreate(email, out var parsed)
        && parsed.Address == email;
}