# user-service

Registers users, checks passwords and issues JWT access tokens. Other services
never call it per request: the gateway verifies tokens with the public key
published at `/.well-known/jwks.json`.

## API

| Method and path | Purpose |
|---|---|
| `POST /auth/register` | `{"email","password"}` → `201 {"id","email"}`; `409` email taken; `422` invalid; `400` malformed or unknown fields |
| `POST /auth/login` | `{"email","password"}` → `200 {"access_token","token_type":"Bearer","expires_in":900}`; `401` invalid credentials |
| `GET /.well-known/jwks.json` | Public signing key (JWKS), cacheable for 5 minutes |
| `GET /healthz`, `GET /readyz` | Liveness (never checks PostgreSQL); readiness (`503` while shutting down) |

Tokens: RS256, 15 minutes, claims `sub` (user id), `email`, `role`, `iss`, `aud`, `exp`.
Passwords: PBKDF2 via ASP.NET Core Identity's `PasswordHasher`; minimum 12 characters.

## Run modes (one image)

| Command | What it does |
|---|---|
| `dotnet UserService.dll` | HTTP server |
| `dotnet UserService.dll migrate` | Applies EF Core migrations, then exits. Runs as a Kubernetes Job before each rollout. |

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `ConnectionStrings__UsersDb` | *(required)* | Npgsql connection string, e.g. `Host=postgres;Database=users;Username=…;Password=…;Timeout=3;GSS Encryption Mode=Disable` |
| `JWT_PRIVATE_KEY` | *(required for the server)* | PEM RSA private key, 2048+ bits. From Secrets Manager in AWS. |
| `JWT_ISSUER` | `stratus-user-service` | `iss` claim |
| `JWT_AUDIENCE` | `stratus-api` | `aud` claim |
| `JWT_LIFETIME_MINUTES` | `15` | Token lifetime |
| `ASPNETCORE_HTTP_PORTS` | `8081` (set in the image) | Listen port |
| `REQUEST_TIMEOUT_SECONDS` | `5` | Upper bound per request; `503` when exceeded |
| `DB_COMMAND_TIMEOUT_SECONDS` | `5` | Upper bound per SQL command |
| `SHUTDOWN_TIMEOUT_SECONDS` | `20` | Time for in-flight requests after SIGTERM |


Missing `ConnectionStrings__UsersDb` or an invalid `JWT_PRIVATE_KEY` stops the
process at startup with exit code 2.

## Test

No .NET SDK needed on the host:

```
docker run --rm -v "$PWD":/src -w /src -v stratus-nuget:/root/.nuget/packages \
  mcr.microsoft.com/dotnet/sdk:10.0 dotnet test UserService.slnx
```

## Known limitations

- `409` on register reveals that an email exists (accepted for now; login does not reveal it).
- No refresh tokens or password reset yet.