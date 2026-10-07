using Microsoft.EntityFrameworkCore;

namespace UserService.Data;

public sealed class User
{
    public Guid Id { get; set; }
    public string Email { get; set; } = "";
    public string PasswordHash { get; set; } = "";
    public DateTimeOffset CreatedAt { get; set; }
}

public sealed class UserDbContext(DbContextOptions<UserDbContext> options) : DbContext(options)
{
    public DbSet<User> Users => Set<User>();

    protected override void OnModelCreating(ModelBuilder modelBuilder)
    {
        modelBuilder.Entity<User>(b =>
        {
            b.ToTable("users");
            b.HasKey(u => u.Id).HasName("pk_users");
            b.Property(u => u.Id).HasColumnName("id").ValueGeneratedNever();
            b.Property(u => u.Email).HasColumnName("email").HasMaxLength(254).IsRequired();
            b.Property(u => u.PasswordHash).HasColumnName("password_hash").IsRequired();
            b.Property(u => u.CreatedAt).HasColumnName("created_at");
            b.HasIndex(u => u.Email).IsUnique().HasDatabaseName("ix_users_email");
        });
    }
}