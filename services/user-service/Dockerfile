

# ---------- build stage ----------
FROM mcr.microsoft.com/dotnet/sdk:10.0 AS build
WORKDIR /src

# Restore first: only the project and lock files, so this layer stays cached
# until dependencies change. --locked-mode fails if packages.lock.json does
# not match the project.
COPY Directory.Build.props ./
COPY src/UserService/UserService.csproj src/UserService/packages.lock.json src/UserService/
RUN dotnet restore src/UserService/UserService.csproj --locked-mode

# Source last, then publish without restoring again.
COPY src/UserService/ src/UserService/
RUN dotnet publish src/UserService/UserService.csproj -c Release --no-restore -o /out /p:UseAppHost=false
#  /out contains:
#   UserService.dll          (your compiled code that runs on any OS or CPU through the .NET runtime)
#   UserService.runtimeconfig.json (Tells dotnet which runtime to use (ASP.NET Core 10.0) and runtime settings such as GC mode. Without it, the app won't start.) 
#   UserService.deps.json (The list of dependencies and where to load each one from)
#   UserService.pdb   (Debug symbols, so stack traces show file and line numbers. Small, and useful during incidents.)
#   All required .dll files   (dependencies)
#   web.config    (Generated for IIS on Windows. Harmless on Linux.)
#  why did we use /p:UseAppHost=false
#   There are two ways to start a .NET app:

#   dotnet UserService.dll   ← the "dotnet" host finds the runtime and runs the DLL
#   ./UserService            ← the "apphost": a small native launcher that does the same thing

#   The apphost is a convenience so you can type ./UserService. We don't need it because ENTRYPOINT ["dotnet", "UserService.dll"] already starts the app. Turning it off has two benefits:

#   One less file. Less in the image means less to scan.
#   The output becomes fully platform-neutral. The DLL is portable bytecode, but the apphost is native machine code for one OS and CPU. On Day 4 we build images for both arm64 (your Mac, Graviton nodes) and amd64. Without an apphost, the same publish output works on both.

#   The /p: syntax means "set this MSBuild property", like -ldflags for go build.

# ---------- runtime stage ----------
# Ubuntu chiseled: Microsoft's distroless .NET image. No shell, no package manager.
FROM mcr.microsoft.com/dotnet/aspnet:10.0-noble-chiseled
WORKDIR /app
COPY --from=build /out ./
ENV ASPNETCORE_HTTP_PORTS=8081
# It tells Kestrel, the ASP.NET Core web server, which port to listen on. Default is 8080.
# Also we can override the port 8081 during run time if we want with -e ASPNETCORE_HTTP_PORTS=xxxx option
USER 1654:1654
EXPOSE 8081
ENTRYPOINT ["dotnet", "UserService.dll"]