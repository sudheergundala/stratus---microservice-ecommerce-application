using Microsoft.EntityFrameworkCore;
using Npgsql;

namespace UserService.Data;

// What the endpoints need from storage. Tests replace it with an in-memory fake.
public interface IUserStore
{
    // Returns false when the email is already registered.
    Task<bool> TryAddAsync(User user, CancellationToken ct);

    Task<User?> FindByEmailAsync(string email, CancellationToken ct);
}

public sealed class EfUserStore(UserDbContext db) : IUserStore
{
    public async Task<bool> TryAddAsync(User user, CancellationToken ct)
    {
        db.Users.Add(user);
        try
        {
            await db.SaveChangesAsync(ct);
            return true;
        }
        catch (DbUpdateException ex) when (ex.InnerException is PostgresException { SqlState: PostgresErrorCodes.UniqueViolation })
        {
            // The unique index on email decides, so two replicas can never
            // register the same email twice.
            return false;
        }
    }

    public Task<User?> FindByEmailAsync(string email, CancellationToken ct) =>
        db.Users.AsNoTracking().SingleOrDefaultAsync(u => u.Email == email, ct);
}