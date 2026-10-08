# ---------- build stage ----------
FROM mcr.microsoft.com/dotnet/sdk:10.0 AS build
WORKDIR /src

# Restore first: cached until dependencies change; --locked-mode enforces the lock file.
COPY Directory.Build.props ./
COPY src/NotificationService/NotificationService.csproj src/NotificationService/packages.lock.json src/NotificationService/
RUN dotnet restore src/NotificationService/NotificationService.csproj --locked-mode

COPY src/NotificationService/ src/NotificationService/
RUN dotnet publish src/NotificationService/NotificationService.csproj -c Release --no-restore -o /out /p:UseAppHost=false

# ---------- runtime stage ----------
FROM mcr.microsoft.com/dotnet/aspnet:10.0-noble-chiseled
WORKDIR /app
COPY --from=build /out ./
# Probe port only: this worker serves no user traffic.
ENV ASPNETCORE_HTTP_PORTS=8087
USER 1654:1654
EXPOSE 8087
ENTRYPOINT ["dotnet", "NotificationService.dll"]