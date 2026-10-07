using System.Collections.Concurrent;
using System.Net;
using System.Net.Http.Json;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Mvc.Testing;
using Microsoft.AspNetCore.TestHost;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.IdentityModel.JsonWebTokens;
using Microsoft.IdentityModel.Tokens;
using Npgsql;
using UserService.Auth;
using UserService.Data;
using Xunit;
using System.Net.Sockets;

namespace UserService.Tests;

// In-memory stand-in for PostgreSQL with the same rule: one account per email.
public sealed class FakeUserStore : IUserStore
{
    private readonly ConcurrentDictionary<string, User> _users = new();

    public Exception? Fail { get; set; }

    public IReadOnlyList<User> Users => _users.Values.ToList();

    public Task<bool> TryAddAsync(User user, CancellationToken ct)
    {
        if (Fail is not null) throw Fail;
        return Task.FromResult(_users.TryAdd(user.Email, user));
    }

    public Task<User?> FindByEmailAsync(string email, CancellationToken ct)
    {
        if (Fail is not null) throw Fail;
        return Task.FromResult(_users.TryGetValue(email, out var user) ? user : null);
    }
}

public sealed class AuthApiTests : IDisposable
{
    private const string Password = "correct-horse-battery";

    private readonly FakeUserStore _store = new();
    private readonly WebApplicationFactory<Program> _baseFactory = new();
    private readonly WebApplicationFactory<Program> _factory;
    private readonly HttpClient _client;

    public AuthApiTests()
    {
        using var rsa = RSA.Create(2048);
        var jwt = new JwtSettings(rsa.ExportPkcs8PrivateKeyPem(), "test-issuer", "test-audience", TimeSpan.FromMinutes(15));

        _factory = _baseFactory.WithWebHostBuilder(b =>
        {
            b.UseSetting("ConnectionStrings:UsersDb", "Host=not-used-by-tests");
            b.ConfigureTestServices(services =>
            {
                services.AddSingleton<IUserStore>(_store);
                services.AddSingleton(jwt);
            });
        });
        _client = _factory.CreateClient();
    }

    public void Dispose()
    {
        _client.Dispose();
        _baseFactory.Dispose(); // also disposes the configured factory
    }

    private Task<HttpResponseMessage> Post(string path, string json) =>
        _client.PostAsync(path, new StringContent(json, Encoding.UTF8, "application/json"));

    private Task<HttpResponseMessage> Register(string email, string password = Password) =>
        _client.PostAsJsonAsync("/auth/register", new { email, password });

    private Task<HttpResponseMessage> Login(string email, string password = Password) =>
        _client.PostAsJsonAsync("/auth/login", new { email, password });

    [Fact]
    public async Task Login_token_verifies_against_the_published_jwks()
    {
        Assert.Equal(HttpStatusCode.Created, (await Register("Ada@Example.com")).StatusCode);

        var login = await Login("ada@example.com");
        Assert.Equal(HttpStatusCode.OK, login.StatusCode);
        using var body = JsonDocument.Parse(await login.Content.ReadAsStringAsync());
        var token = body.RootElement.GetProperty("access_token").GetString()!;
        Assert.Equal(900, body.RootElement.GetProperty("expires_in").GetInt32());

        // Verify exactly as the gateway will: public key from the JWKS endpoint,
        // plus issuer, audience and expiry.
        var jwks = new JsonWebKeySet(await _client.GetStringAsync("/.well-known/jwks.json"));
        var result = await new JsonWebTokenHandler().ValidateTokenAsync(token, new TokenValidationParameters
        {
            ValidIssuer = "test-issuer",
            ValidAudience = "test-audience",
            IssuerSigningKeys = jwks.GetSigningKeys(),
        });

        Assert.True(result.IsValid, result.Exception?.Message);
        Assert.Equal(_store.Users.Single().Id.ToString(), (string)result.Claims["sub"]);
    }

    [Fact]
    public async Task Password_is_stored_hashed()
    {
        await Register("ada@example.com");
        var stored = _store.Users.Single().PasswordHash;
        Assert.NotEqual(Password, stored);
        Assert.DoesNotContain(Password, stored);
    }

    [Fact]
    public async Task Same_email_cannot_register_twice()
    {
        await Register("ada@example.com");
        var again = await Register("  ADA@example.com ");
        Assert.Equal(HttpStatusCode.Conflict, again.StatusCode);
        Assert.Single(_store.Users);
    }

    [Fact]
    public async Task Wrong_password_and_unknown_email_look_the_same()
    {
        await Register("ada@example.com");
        var wrongPassword = await Login("ada@example.com", "not-the-right-password");
        var unknownEmail = await Login("nobody@example.com");

        Assert.Equal(HttpStatusCode.Unauthorized, wrongPassword.StatusCode);
        Assert.Equal(HttpStatusCode.Unauthorized, unknownEmail.StatusCode);
        Assert.Equal(await wrongPassword.Content.ReadAsStringAsync(), await unknownEmail.Content.ReadAsStringAsync());
    }

    [Theory]
    [InlineData("""{"email":"not-an-email","password":"long-enough-password"}""", 422)]
    [InlineData("""{"email":"ada@example.com","password":"short"}""", 422)]
    [InlineData("""{"email":"ada@example.com","password":"long-enough-password","role":"admin"}""", 400)]
    [InlineData("""{"email":""", 400)]
    public async Task Register_rejects_invalid_input(string json, int expected)
    {
        var response = await Post("/auth/register", json);
        Assert.Equal(expected, (int)response.StatusCode);
        Assert.Empty(_store.Users);
    }

    [Fact]
    public async Task Database_failure_returns_503()
    {
        _store.Fail = new NpgsqlException("connection refused");
        var response = await Login("ada@example.com");
        Assert.Equal(HttpStatusCode.ServiceUnavailable, response.StatusCode);
    }

    [Fact]
    public async Task Probes_and_jwks_cache_header()
    {
        Assert.Equal(HttpStatusCode.OK, (await _client.GetAsync("/healthz")).StatusCode);
        Assert.Equal(HttpStatusCode.OK, (await _client.GetAsync("/readyz")).StatusCode);

        var jwks = await _client.GetAsync("/.well-known/jwks.json");
        Assert.True(jwks.Headers.CacheControl?.Public);
        Assert.Equal(TimeSpan.FromMinutes(5), jwks.Headers.CacheControl?.MaxAge);
    }
        [Fact]
    public async Task Wrapped_database_failure_returns_503()
    {
        // What a real outage looks like: EF's retry wrapper -> Npgsql -> socket error.
        _store.Fail = new InvalidOperationException("retry limit exceeded",
            new NpgsqlException("failed to connect", new SocketException((int)SocketError.ConnectionRefused)));
        var response = await Login("ada@example.com");
        Assert.Equal(HttpStatusCode.ServiceUnavailable, response.StatusCode);
    }
}