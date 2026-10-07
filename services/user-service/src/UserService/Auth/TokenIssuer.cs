using System.Security.Cryptography;
using System.Text;
using Microsoft.IdentityModel.JsonWebTokens;
using Microsoft.IdentityModel.Tokens;
using UserService.Data;

namespace UserService.Auth;

public sealed record JwtSettings(string PrivateKeyPem, string Issuer, string Audience, TimeSpan Lifetime);

// Signs RS256 access tokens and publishes the matching public key as a JWKS,
// so the gateway can verify tokens without calling this service.
public sealed class TokenIssuer : IDisposable
{
    private readonly RSA _rsa = RSA.Create();
    private readonly JsonWebTokenHandler _handler = new();
    private readonly SigningCredentials _credentials;
    private readonly JwtSettings _settings;
    private readonly TimeProvider _time;

    public string KeyId { get; }

    // Public key only. Safe to publish.
    public object Jwks { get; }

    public TokenIssuer(JwtSettings settings, TimeProvider time)
    {
        _settings = settings;
        _time = time;

        if (!settings.PrivateKeyPem.Contains("PRIVATE KEY", StringComparison.Ordinal))
        {
            throw new InvalidOperationException("JWT_PRIVATE_KEY must be a PEM-encoded RSA private key");
        }
        try
        {
            _rsa.ImportFromPem(settings.PrivateKeyPem);
        }
        catch (Exception ex) when (ex is ArgumentException or CryptographicException)
        {
            throw new InvalidOperationException("JWT_PRIVATE_KEY could not be read as an RSA private key", ex);
        }
        if (_rsa.KeySize < 2048)
        {
            throw new InvalidOperationException("JWT_PRIVATE_KEY must be at least 2048 bits");
        }

        var publicKey = _rsa.ExportParameters(includePrivateParameters: false);
        var n = Base64UrlEncoder.Encode(publicKey.Modulus!);
        var e = Base64UrlEncoder.Encode(publicKey.Exponent!);

        // Key id = RFC 7638 thumbprint of the public key: stable across restarts
        // and replicas, and it changes automatically when the key is rotated.
        var canonical = "{\"e\":\"" + e + "\",\"kty\":\"RSA\",\"n\":\"" + n + "\"}";
        KeyId = Base64UrlEncoder.Encode(SHA256.HashData(Encoding.UTF8.GetBytes(canonical)));

        Jwks = new { keys = new[] { new { kty = "RSA", use = "sig", alg = "RS256", kid = KeyId, n, e } } };
        _credentials = new SigningCredentials(new RsaSecurityKey(_rsa) { KeyId = KeyId }, SecurityAlgorithms.RsaSha256);
    }

    public (string Token, int ExpiresInSeconds) Issue(User user)
    {
        var now = _time.GetUtcNow().UtcDateTime;
        var token = _handler.CreateToken(new SecurityTokenDescriptor
        {
            Issuer = _settings.Issuer,
            Audience = _settings.Audience,
            IssuedAt = now,
            NotBefore = now,
            Expires = now.Add(_settings.Lifetime),
            SigningCredentials = _credentials,
            Claims = new Dictionary<string, object>
            {
                [JwtRegisteredClaimNames.Sub] = user.Id.ToString(),
                [JwtRegisteredClaimNames.Email] = user.Email,
                [JwtRegisteredClaimNames.Jti] = Guid.NewGuid().ToString(),
                ["role"] = "customer",
            },
        });
        return (token, (int)_settings.Lifetime.TotalSeconds);
    }

    public void Dispose() => _rsa.Dispose();
}