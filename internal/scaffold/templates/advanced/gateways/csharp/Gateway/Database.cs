// Demo code from pgmi. Do not copy this file into the project as is. Read it,
// then write your own gateway that fits this project: it is a baseline to
// learn from, not a library.
//
// The one connection pool of the process (../../PROTOCOL.md, "Connections").

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
    // translated, an Npgsql string passes through.
    public static string ConnectionString(string databaseUrl)
    {
        if (!databaseUrl.StartsWith("postgres://") && !databaseUrl.StartsWith("postgresql://"))
        {
            return databaseUrl;
        }
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
        // libpq query parameters. sslmode keeps its libpq meaning: require
        // encrypts without checking the certificate, verify-full checks the
        // chain against sslrootcert and the host name. An unknown parameter is
        // refused rather than dropped, so a TLS setting cannot vanish.
        foreach (var pair in uri.Query.TrimStart('?').Split('&', StringSplitOptions.RemoveEmptyEntries))
        {
            var kv = pair.Split('=', 2);
            var key = Uri.UnescapeDataString(kv[0]);
            var value = kv.Length > 1 ? Uri.UnescapeDataString(kv[1]) : "";
            switch (key)
            {
                case "sslmode":
                    csb.SslMode = value switch
                    {
                        "disable" => SslMode.Disable,
                        "allow" => SslMode.Allow,
                        "prefer" => SslMode.Prefer,
                        "require" => SslMode.Require,
                        "verify-ca" => SslMode.VerifyCA,
                        "verify-full" => SslMode.VerifyFull,
                        _ => throw new ArgumentException($"sslmode={value} is not a libpq sslmode"),
                    };
                    break;
                case "sslrootcert": csb.RootCertificate = value; break;
                case "sslcert": csb.SslCertificate = value; break;
                case "sslkey": csb.SslKey = value; break;
                case "sslpassword": csb.SslPassword = value; break;
                case "connect_timeout": csb.Timeout = int.Parse(value); break;
                case "options": csb.Options = value; break;
                default: throw new ArgumentException($"DATABASE_URL parameter {key} is not supported by this gateway");
            }
        }
        return csb.ConnectionString;
    }
}
