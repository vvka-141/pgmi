// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// The one connection pool of the process (../../PROTOCOL.md, "Connections").

using System.Data.Common;
using System.Security.Cryptography.X509Certificates;
using Npgsql;

namespace Pgmi.Gateway;

public static class Database
{
    // ADAPT: the name operators see in pg_stat_activity and the server log.
    public const string ApplicationName = "pgmi-gateway-csharp";

    // Bounded, with a connect and an acquire timeout: an exhausted pool is a
    // 503 after PoolTimeout, never a request that hangs. ADAPT: the server's
    // max_connections is shared by every gateway process.
    public static NpgsqlDataSource CreateDataSource(string databaseUrl, int maxPoolSize = 10, ILoggerFactory? loggers = null)
    {
        var builder = new NpgsqlDataSourceBuilder(ConnectionString(databaseUrl));
        if (builder.ConnectionStringBuilder.RootCertificate == "system")
        {
            builder.ConnectionStringBuilder.RootCertificate = null;
            builder.UseRootCertificates(SystemRoots());
        }
        builder.ConnectionStringBuilder.ApplicationName = ApplicationName;
        builder.ConnectionStringBuilder.MaxPoolSize = maxPoolSize;
        builder.ConnectionStringBuilder.Timeout = (int)GatewayOptions.PoolTimeout.TotalSeconds;
        builder.ConnectionStringBuilder.ConnectionIdleLifetime = 30;
        if (loggers is not null)
        {
            builder.UseLoggerFactory(loggers);
        }
        return builder.Build();
    }

    // Npgsql reads key=value connection strings; a postgresql:// URL is
    // translated, an Npgsql string passes through. Either way the TLS
    // settings are resolved as libpq resolves them (Tls below).
    public static string ConnectionString(string databaseUrl)
    {
        string? sslmode = null;
        var isUrl = databaseUrl.StartsWith("postgres://") || databaseUrl.StartsWith("postgresql://");
        var csb = isUrl ? FromUrl(databaseUrl, out sslmode) : new NpgsqlConnectionStringBuilder(databaseUrl);
        var rootcert = csb.RootCertificate
            ?? Environment.GetEnvironmentVariable("PGSSLROOTCERT")
            ?? DefaultRootCertificate();
        if (sslmode is null && (isUrl || !NamesSslMode(databaseUrl)))
        {
            sslmode = Environment.GetEnvironmentVariable("PGSSLMODE") is { Length: > 0 } fromEnvironment ? fromEnvironment : null;
        }
        sslmode ??= rootcert == "system" ? "verify-full" : LibpqSslmode(csb.SslMode);
        csb.SslMode = Tls(sslmode, rootcert);
        csb.RootCertificate = rootcert;
        return csb.ConnectionString;
    }

    // libpq sslmode: prefer and require encrypt without checking the
    // certificate (require with sslrootcert checks the chain, as verify-ca),
    // verify-ca checks the chain against sslrootcert, verify-full also the
    // host name.
    //
    // sslrootcert is the only trust anchor, as in libpq: from DATABASE_URL,
    // else PGSSLROOTCERT, else ~/.postgresql/root.crt when it exists. Npgsql
    // would check a verify mode without one against the system store, so that
    // is refused. sslrootcert=system means the system store, with verify-full
    // only.
    public static SslMode Tls(string sslmode, string? rootcert)
    {
        if (rootcert == "system" && sslmode != "verify-full")
        {
            throw new ArgumentException($"sslrootcert=system needs sslmode=verify-full, not {sslmode}");
        }
        if (rootcert is null && sslmode is "verify-ca" or "verify-full")
        {
            throw new ArgumentException($"sslmode={sslmode} needs sslrootcert in DATABASE_URL: the CA file to trust, or system");
        }
        return sslmode switch
        {
            "disable" => SslMode.Disable,
            "allow" => SslMode.Allow,
            "prefer" => SslMode.Prefer,
            "require" => rootcert is null ? SslMode.Require : SslMode.VerifyCA,
            "verify-ca" => SslMode.VerifyCA,
            "verify-full" => SslMode.VerifyFull,
            _ => throw new ArgumentException($"sslmode={sslmode} is not a libpq sslmode"),
        };
    }

    static bool NamesSslMode(string connectionString) =>
        new DbConnectionStringBuilder { ConnectionString = connectionString }.Keys.Cast<string>()
            .Any(key => key.Replace(" ", "").Replace("_", "").Equals("sslmode", StringComparison.OrdinalIgnoreCase));

    static string LibpqSslmode(SslMode mode) => mode switch
    {
        SslMode.VerifyCA => "verify-ca",
        SslMode.VerifyFull => "verify-full",
        _ => mode.ToString().ToLowerInvariant(),
    };

    static string? DefaultRootCertificate()
    {
        var home = Environment.GetEnvironmentVariable(OperatingSystem.IsWindows() ? "APPDATA" : "HOME");
        var path = home is null ? null : Path.Combine(home, OperatingSystem.IsWindows() ? "postgresql" : ".postgresql", "root.crt");
        return File.Exists(path) ? path : null;
    }

    static X509Certificate2Collection SystemRoots()
    {
        using var store = new X509Store(StoreName.Root, StoreLocation.LocalMachine);
        store.Open(OpenFlags.ReadOnly);
        return store.Certificates;
    }

    static NpgsqlConnectionStringBuilder FromUrl(string databaseUrl, out string? sslmode)
    {
        sslmode = null;
        var uri = new Uri(databaseUrl);
        var user = uri.UserInfo.Split(':', 2);
        var csb = new NpgsqlConnectionStringBuilder
        {
            Host = uri.Host,
            Port = uri.IsDefaultPort || uri.Port < 0 ? 5432 : uri.Port,
            Database = Uri.UnescapeDataString(uri.AbsolutePath.TrimStart('/')),
            Username = Uri.UnescapeDataString(user[0]),
        };
        if (user.Length > 1)
        {
            csb.Password = Uri.UnescapeDataString(user[1]);
        }
        // libpq query parameters. An unknown one is refused rather than
        // dropped, so a TLS setting cannot vanish.
        foreach (var pair in uri.Query.TrimStart('?').Split('&', StringSplitOptions.RemoveEmptyEntries))
        {
            var kv = pair.Split('=', 2);
            var key = Uri.UnescapeDataString(kv[0]);
            var value = kv.Length > 1 ? Uri.UnescapeDataString(kv[1]) : "";
            switch (key)
            {
                case "sslmode": sslmode = value; break;
                case "sslrootcert": csb.RootCertificate = value.Length > 0 ? value : null; break;
                case "sslcert": csb.SslCertificate = value; break;
                case "sslkey": csb.SslKey = value; break;
                case "sslpassword": csb.SslPassword = value; break;
                case "connect_timeout": csb.Timeout = int.Parse(value); break;
                case "options": csb.Options = value; break;
                default: throw new ArgumentException($"DATABASE_URL parameter {key} is not supported by this gateway");
            }
        }
        return csb;
    }
}
