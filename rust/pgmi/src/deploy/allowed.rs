//! `pgmi serve`: the servers the operator's credentials may go to, and the
//! connection settings a tool call may not lower when they do (PGMI-915,
//! PGMI-1005).

use std::net::{IpAddr, Ipv6Addr};

use crate::conn::cloud::{Options, Provider};
use crate::conn::resolve::{
    self, ChannelBinding, Endpoint, RequireAuth, Spec, SslCertMode, SslMode,
};
use crate::conn::tap;
use crate::error::{Error, Result};

/// Without the operator's own settings there is nothing to hold a tool call
/// to, while `PGMI_SERVE_HOSTS` would still hand out its credentials.
pub(crate) const NO_RULES: &str = "pgmi serve does not start: the connection settings of its own environment do not resolve, and they are the rules a tool call is held to";

/// The operator's allowed servers and its own connection settings.
#[derive(Debug, Clone, Default)]
pub(crate) struct ServeHosts {
    pub(crate) hosts: Vec<AllowedHost>,
    pub(crate) floor: Option<Floor>,
    /// Cloud SQL instance connection names: the Google route dials the
    /// instance, not a host, so the instance is what the operator allows.
    pub(crate) instances: Vec<String>,
}

/// One allowed server: a host, and a port when the entry named one.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct AllowedHost {
    host: String,
    port: Option<u16>,
}

/// The operator's own `sslmode`, `sslrootcert`, `channel_binding` and
/// `require_auth`.
#[derive(Debug, Clone)]
pub(crate) struct Floor {
    sslmode: SslMode,
    sslrootcert: Option<String>,
    channel_binding: ChannelBinding,
    require_auth: Option<tap::Policy>,
}

impl ServeHosts {
    /// `PGMI_SERVE_HOSTS` entries, then the endpoints of the operator's own
    /// connection, whose settings are the floor.
    pub(crate) fn new(entries: Option<&str>, own: Option<&Spec>) -> Result<Self> {
        let mut hosts = entries
            .unwrap_or_default()
            .split(',')
            .map(str::trim)
            .filter(|e| !e.is_empty())
            .map(AllowedHost::parse)
            .collect::<Result<Vec<_>>>()?;
        hosts.extend(own.into_iter().flat_map(|s| {
            s.hosts.endpoints().map(|e| AllowedHost {
                host: e.name.clone(),
                port: Some(e.port),
            })
        }));
        let floor = own
            .map(|s| {
                Ok::<_, Error>(Floor {
                    sslmode: s.sslmode(),
                    sslrootcert: s.sslrootcert.clone(),
                    channel_binding: s.channel_binding,
                    require_auth: s
                        .require_auth
                        .policy()
                        .map_err(|e| Error::config(format!("{NO_RULES}: {e}")))?
                        .cloned(),
                })
            })
            .transpose()?;
        Ok(ServeHosts {
            hosts,
            floor,
            instances: Vec::new(),
        })
    }

    /// Adds the `PGMI_SERVE_CLOUDSQL_INSTANCES` entries, compared whole.
    #[must_use]
    pub(crate) fn with_instances(mut self, entries: Option<&str>) -> Self {
        self.instances = entries
            .unwrap_or_default()
            .split(',')
            .map(str::trim)
            .filter(|e| !e.is_empty())
            .map(str::to_string)
            .collect();
        self
    }

    fn allows(&self, e: &Endpoint) -> bool {
        self.hosts.iter().any(|a| a.matches(e))
    }
}

impl AllowedHost {
    /// `host`, `host:port`, `[v6]` or `[v6]:port`. An IPv6 address must be
    /// in brackets: `::1:5432` is itself an address, so it cannot say a port.
    pub(crate) fn parse(entry: &str) -> Result<Self> {
        let refuse = |why: &str| {
            Error::config(format!(
                "PGMI_SERVE_HOSTS entry \"{entry}\": {why}; write host, host:port or [ipv6]:port"
            ))
        };
        let (host, port) = if let Some(rest) = entry.strip_prefix('[') {
            let (v6, after) = rest
                .split_once(']')
                .ok_or_else(|| refuse("the bracket is not closed"))?;
            if v6.parse::<Ipv6Addr>().is_err() {
                return Err(refuse("not an IPv6 address in the brackets"));
            }
            match after {
                "" => (v6, None),
                p => (
                    v6,
                    Some(
                        p.strip_prefix(':')
                            .ok_or_else(|| refuse("a port follows a colon"))?,
                    ),
                ),
            }
        } else if entry.matches(':').count() > 1 {
            return Err(refuse("an IPv6 address must be written in brackets"));
        } else {
            match entry.split_once(':') {
                Some((h, p)) => (h, Some(p)),
                None => (entry, None),
            }
        };
        if host.is_empty() {
            return Err(refuse("the host is empty"));
        }
        let port = port
            .map(|p| {
                p.parse::<u16>()
                    .ok()
                    .filter(|p| *p != 0)
                    .ok_or_else(|| refuse("the port is not 1-65535"))
            })
            .transpose()?;
        Ok(AllowedHost {
            host: host.to_string(),
            port,
        })
    }

    fn matches(&self, e: &Endpoint) -> bool {
        let host = match (self.host.parse::<IpAddr>(), e.name.parse::<IpAddr>()) {
            (Ok(a), Ok(b)) => a == b,
            _ => self.host.eq_ignore_ascii_case(&e.name),
        };
        host && self.port.is_none_or(|p| p == e.port)
    }
}

fn sslmode_rank(m: SslMode) -> u8 {
    match m {
        SslMode::Disable => 0,
        SslMode::Allow => 1,
        SslMode::Prefer => 2,
        SslMode::Require => 3,
        SslMode::VerifyCa => 4,
        SslMode::VerifyFull => 5,
    }
}

fn binding_rank(b: ChannelBinding) -> u8 {
    match b {
        ChannelBinding::Disable => 0,
        ChannelBinding::Prefer => 1,
        ChannelBinding::Require => 2,
    }
}

impl Floor {
    /// Raises `spec`'s `sslmode` and `channel_binding` to the operator's, and
    /// takes the operator's `require_auth` when the call set none; a call
    /// whose `require_auth` allows a method the operator's does not is
    /// refused, and so is one whose `sslrootcert` is not the operator's:
    /// the root certificate decides which server passes verification.
    /// `sent` says which credential goes: a typed password replaces the
    /// operator's password, never its cloud sign-in.
    fn raise(&self, spec: &mut Spec, sent: Sent) -> Result<()> {
        if spec.sslrootcert != self.sslrootcert {
            let shown = |c: &Option<String>| {
                c.as_ref()
                    .map_or_else(|| "unset".to_string(), |c| format!("\"{c}\""))
            };
            return Err(Error::config(format!(
                "sslrootcert {} is not the operator's own ({}), and pgmi serve would send {} to a server it verifies with another root certificate; leave sslrootcert out{}",
                shown(&spec.sslrootcert),
                shown(&self.sslrootcert),
                sent.what(),
                match sent {
                    Sent::Password =>
                        ", set it in the environment pgmi serve runs in, or pass a password in the connection string",
                    Sent::CloudSignIn => " or set it in the environment pgmi serve runs in",
                }
            )));
        }
        if sslmode_rank(spec.sslmode()) < sslmode_rank(self.sslmode) {
            spec.requested_sslmode = Some(self.sslmode);
        }
        if binding_rank(spec.channel_binding) < binding_rank(self.channel_binding) {
            spec.channel_binding = self.channel_binding;
        }
        let Some(floor) = &self.require_auth else {
            return Ok(());
        };
        match spec.require_auth.policy() {
            Ok(None) => spec.require_auth = RequireAuth::Only(floor.clone()),
            Ok(Some(own)) if !own.within(floor) => {
                return Err(Error::config(format!(
                    "require_auth \"{}\" allows a sign-in method the operator's own require_auth \"{}\" does not, and pgmi serve would send {}; narrow it{}",
                    own.text(),
                    floor.text(),
                    sent.what(),
                    match sent {
                        Sent::Password => " or pass a password in the connection string",
                        Sent::CloudSignIn => "",
                    }
                )));
            }
            _ => {}
        }
        Ok(())
    }
}

/// The operator's credential a connection to an allowed server carries.
#[derive(Debug, Clone, Copy)]
enum Sent {
    Password,
    CloudSignIn,
}

impl Sent {
    fn what(self) -> &'static str {
        match self {
            Sent::Password => "the operator's credentials",
            Sent::CloudSignIn => "the operator's cloud sign-in token",
        }
    }
}

/// For `pgmi serve`: the first server not on the operator's list, whose
/// connection then gets none of the operator's credentials. A cloud
/// sign-in to such a server is refused. On an allowed server the
/// operator's credentials go only with settings at least as strict as the
/// operator's own. A password the call typed takes the place of the
/// operator's password, so the call then keeps its own settings; it does not
/// take the place of a cloud sign-in, whose token is what the server gets.
pub(crate) fn restrict_to_allowed(
    spec: &mut Spec,
    flags: &resolve::Flags,
    cloud: Option<&(Provider, Options)>,
    allowed: &ServeHosts,
) -> Result<Option<String>> {
    if let Some((Provider::Google, options)) = cloud {
        let instance = options.google_instance.as_deref().unwrap_or_default();
        if !allowed.instances.iter().any(|i| i == instance) {
            return Err(Error::config(format!(
                "pgmi serve signs in to Cloud SQL only for instances the operator allowed, and \"{instance}\" is not one; add it to PGMI_SERVE_CLOUDSQL_INSTANCES where pgmi serve starts"
            )));
        }
    }
    let typed = flags.connection.as_ref().is_some_and(|c| {
        crate::conn::parse::parse(c.expose()).is_ok_and(|f| f.contains_key("password"))
    });
    let Some(host) = spec
        .hosts
        .endpoints()
        .find(|e| !allowed.allows(e))
        .map(|e| format!("{}:{}", e.name, e.port))
    else {
        let sent = match (cloud, typed) {
            (Some(_), _) => Some(Sent::CloudSignIn),
            (None, false) => Some(Sent::Password),
            (None, true) => None,
        };
        if let (Some(sent), Some(floor)) = (sent, &allowed.floor) {
            floor.raise(spec, sent)?;
        }
        return Ok(None);
    };
    if cloud.is_some() {
        return Err(Error::config(format!(
            "pgmi serve signs in to a cloud only for hosts the operator allowed, and \"{host}\" is not one; add it to PGMI_SERVE_HOSTS where pgmi serve starts"
        )));
    }
    spec.ambient_passfile = false;
    let own_certificate = flags.connection.as_ref().is_some_and(|c| {
        crate::conn::parse::parse(c.expose()).is_ok_and(|f| {
            ["sslcert", "sslkey"]
                .iter()
                .all(|k| f.get(*k).is_some_and(|v| !v.is_empty()))
        })
    });
    if !own_certificate {
        spec.sslcertmode = SslCertMode::Disable;
    }
    if typed {
        return Ok(None);
    }
    spec.password = None;
    spec.withheld = true;
    Ok(Some(host))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::env::Env;
    use crate::error::Secret;

    fn resolved(conn: &str, env: &[(&str, &str)]) -> (Spec, resolve::Flags) {
        let flags = resolve::Flags {
            connection: Some(conn.into()),
            ..resolve::Flags::default()
        };
        (
            resolve::resolve(&flags, &Env::from_pairs(env), None).unwrap(),
            flags,
        )
    }

    #[test]
    fn a_host_not_allowed_gets_no_client_certificate_of_the_operator() {
        let allowed = ServeHosts::new(Some("db.internal"), None).unwrap();
        let env = [
            ("PGSSLCERT", "/operator/client.crt"),
            ("PGSSLKEY", "/operator/client.key"),
        ];
        let mode = |conn: &str| {
            let (mut spec, flags) = resolved(conn, &env);
            restrict_to_allowed(&mut spec, &flags, None, &allowed).unwrap();
            spec.sslcertmode
        };
        assert_eq!(
            mode("host=evil.example user=u dbname=d"),
            SslCertMode::Disable
        );
        assert_eq!(
            mode("host=evil.example user=u password=typed dbname=d"),
            SslCertMode::Disable
        );
        assert_ne!(
            mode("host=evil.example user=u dbname=d sslcert=/mine.crt sslkey=/mine.key"),
            SslCertMode::Disable
        );
        for conn in [
            "host=evil.example user=u dbname=d sslcert=",
            "host=evil.example user=u dbname=d sslcert= sslkey=",
            "host=evil.example user=u dbname=d sslcert=/operator/client.crt",
            "host=evil.example user=u dbname=d sslkey=/mine.key",
            "host=evil.example user=u dbname=d sslcert=/mine.crt sslkey=",
            "postgresql://u@evil.example/d?sslcert=&sslkey=",
            "postgresql://u@evil.example/d?sslcert=%2Fmine.crt",
        ] {
            assert_eq!(mode(conn), SslCertMode::Disable, "{conn}");
        }
        assert_ne!(
            mode("host=db.internal user=u dbname=d"),
            SslCertMode::Disable
        );
    }

    fn azure() -> (Provider, Options) {
        (Provider::Azure, Options::default())
    }

    fn sign_ins() -> [(Provider, Options); 4] {
        [
            (Provider::Aws, Options::default()),
            azure(),
            (Provider::Databricks, Options::default()),
            google("proj:region:db"),
        ]
    }

    fn operator_with_every_rule() -> ServeHosts {
        let (own, _) = resolved(
            "host=db user=u dbname=d",
            &[
                ("PGSSLMODE", "verify-ca"),
                ("PGSSLROOTCERT", "/op/ca.crt"),
                ("PGCHANNELBINDING", "require"),
                ("PGREQUIREAUTH", "scram-sha-256"),
            ],
        );
        ServeHosts::new(None, Some(&own))
            .unwrap()
            .with_instances(Some("proj:region:db"))
    }

    const TYPED: [&str; 4] = [
        "host=db user=u password=typed dbname=d",
        "postgresql://u:typed@db/d?",
        "postgresql://u@db/d?password=typed&",
        "Host=db;Username=u;Password=typed;Database=d;",
    ];

    fn with(typed: &str, keyword: &str, uri: &str, ado: &str) -> String {
        if typed.starts_with("postgresql://") {
            format!("{typed}{uri}")
        } else if typed.starts_with("Host=") {
            format!("{typed}{ado}")
        } else {
            format!("{typed} {keyword}")
        }
    }

    #[test]
    fn a_typed_password_does_not_swap_the_root_certificate_a_cloud_token_is_verified_with() {
        let allowed = operator_with_every_rule();
        for cloud in sign_ins() {
            for typed in TYPED {
                for conn in [
                    with(
                        typed,
                        "sslrootcert=/mitm/ca.crt",
                        "sslrootcert=%2Fmitm%2Fca.crt",
                        "Root Certificate=/mitm/ca.crt",
                    ),
                    with(
                        typed,
                        "sslrootcert=system",
                        "sslrootcert=system",
                        "Root Certificate=system",
                    ),
                ] {
                    let (mut spec, flags) = resolved(&conn, &[]);
                    let e = restrict_to_allowed(&mut spec, &flags, Some(&cloud), &allowed)
                        .expect_err(&format!("{:?} {conn}", cloud.0));
                    assert_eq!(e.exit_code(), 10, "{:?} {conn}", cloud.0);
                    assert!(
                        e.text()
                            .contains("is not the operator's own (\"/op/ca.crt\")")
                            && e.text().contains("cloud sign-in")
                            && !e.text().contains("pass a password"),
                        "{:?} {conn}: {}",
                        cloud.0,
                        e.text()
                    );
                }
            }
        }
    }

    #[test]
    fn a_typed_password_does_not_lower_sslmode_or_channel_binding_under_a_cloud_sign_in() {
        let allowed = operator_with_every_rule();
        for cloud in sign_ins() {
            for typed in TYPED {
                let conn = with(
                    typed,
                    "sslmode=disable channel_binding=disable sslrootcert=/op/ca.crt",
                    "sslmode=disable&channel_binding=disable&sslrootcert=%2Fop%2Fca.crt",
                    "SSL Mode=Disable;Channel Binding=disable;Root Certificate=/op/ca.crt",
                );
                let (mut spec, flags) = resolved(&conn, &[]);
                assert_eq!(
                    restrict_to_allowed(&mut spec, &flags, Some(&cloud), &allowed).unwrap(),
                    None,
                    "{:?} {conn}",
                    cloud.0
                );
                assert_eq!(spec.sslmode(), SslMode::VerifyCa, "{:?} {conn}", cloud.0);
                assert_eq!(
                    spec.channel_binding,
                    ChannelBinding::Require,
                    "{:?} {conn}",
                    cloud.0
                );
                assert_eq!(
                    spec.require_auth.policy().unwrap().map(tap::Policy::text),
                    Some("scram-sha-256"),
                    "{:?} {conn}",
                    cloud.0
                );
            }
        }
    }

    #[test]
    fn a_typed_password_does_not_widen_require_auth_under_a_cloud_sign_in() {
        let allowed = operator_with_every_rule();
        for cloud in sign_ins() {
            for typed in TYPED {
                let conn = with(
                    typed,
                    "require_auth=password sslrootcert=/op/ca.crt",
                    "require_auth=password&sslrootcert=%2Fop%2Fca.crt",
                    "require_auth=password;Root Certificate=/op/ca.crt",
                );
                let (mut spec, flags) = resolved(&conn, &[]);
                let e = restrict_to_allowed(&mut spec, &flags, Some(&cloud), &allowed)
                    .expect_err(&format!("{:?} {conn}", cloud.0));
                assert_eq!(e.exit_code(), 10, "{:?} {conn}", cloud.0);
                assert!(
                    e.text().contains("require_auth \"password\"")
                        && e.text().contains("cloud sign-in")
                        && !e.text().contains("pass a password"),
                    "{:?} {conn}: {}",
                    cloud.0,
                    e.text()
                );
            }
        }
    }

    #[test]
    fn a_typed_password_keeps_its_own_settings_only_without_a_cloud_sign_in() {
        let allowed = operator_with_every_rule();
        for typed in TYPED {
            let conn = with(
                typed,
                "sslmode=disable sslrootcert=/mine/ca.crt require_auth=password",
                "sslmode=disable&sslrootcert=%2Fmine%2Fca.crt&require_auth=password",
                "SSL Mode=Disable;Root Certificate=/mine/ca.crt;require_auth=password",
            );
            let (mut spec, flags) = resolved(&conn, &[("PGPASSWORD", "operator-pw")]);
            assert_eq!(
                restrict_to_allowed(&mut spec, &flags, None, &allowed).unwrap(),
                None,
                "{conn}"
            );
            assert_eq!(spec.sslmode(), SslMode::Disable, "{conn}");
            assert_eq!(spec.password.as_ref().map(Secret::expose), Some("typed"));
            assert!(spec.token.is_none());
        }
    }

    #[test]
    fn an_empty_typed_password_is_sent_as_written_and_never_replaced_by_the_operators() {
        let allowed = operator_with_every_rule();
        for conn in [
            "host=db user=u password='' dbname=d sslrootcert=/mine/ca.crt",
            "postgresql://u:@db/d?sslrootcert=%2Fmine%2Fca.crt",
            "postgresql://u@db/d?password=&sslrootcert=%2Fmine%2Fca.crt",
            "Host=db;Username=u;Password=;Database=d;Root Certificate=/mine/ca.crt",
            "host=evil.example user=u password='' dbname=d",
        ] {
            let (mut spec, flags) = resolved(conn, &[("PGPASSWORD", "operator-pw")]);
            match restrict_to_allowed(&mut spec, &flags, None, &allowed) {
                Err(e) => assert_eq!(e.exit_code(), 10, "{conn}"),
                Ok(_) => assert_ne!(
                    spec.password.as_ref().map(Secret::expose),
                    Some("operator-pw"),
                    "{conn}"
                ),
            }
        }
    }

    fn google(instance: &str) -> (Provider, Options) {
        let options = Options {
            google_instance: Some(instance.into()),
            ..Options::default()
        };
        (Provider::Google, options)
    }

    #[test]
    fn serve_refuses_a_cloud_sql_instance_the_operator_did_not_allow() {
        let own = resolved("host=localhost user=u dbname=d", &[]).0;
        for listed in [None, Some("proj:region:other, proj:region:Evil")] {
            let allowed = ServeHosts::new(None, Some(&own))
                .unwrap()
                .with_instances(listed);
            let (mut spec, flags) = resolved("user=u dbname=d", &[]);
            let e = restrict_to_allowed(
                &mut spec,
                &flags,
                Some(&google("proj:region:evil")),
                &allowed,
            )
            .unwrap_err();
            assert_eq!(e.exit_code(), 10);
            assert!(
                e.text().contains("\"proj:region:evil\" is not one")
                    && e.text().contains("PGMI_SERVE_CLOUDSQL_INSTANCES"),
                "{}",
                e.text()
            );
        }
    }

    #[test]
    fn serve_allows_a_cloud_sql_instance_the_operator_listed() {
        let allowed = ServeHosts::new(None, None)
            .unwrap()
            .with_instances(Some(" proj:region:db ,other:r:i"));
        let (mut spec, flags) = resolved("user=u dbname=d", &[]);
        let cloud = google("proj:region:db");
        let own = ServeHosts {
            hosts: vec![AllowedHost::parse("localhost").unwrap()],
            ..allowed
        };
        assert_eq!(
            restrict_to_allowed(&mut spec, &flags, Some(&cloud), &own).unwrap(),
            None
        );
    }

    #[test]
    fn serve_keeps_the_operators_credentials_for_the_hosts_the_operator_allowed() {
        let allowed = ServeHosts::new(Some("db.internal, 127.0.0.1:5432"), None).unwrap();
        let env = [("PGPASSWORD", "operator-pw")];

        let (mut spec, flags) = resolved("host=evil.example user=u dbname=d", &env);
        let refused = restrict_to_allowed(&mut spec, &flags, None, &allowed).unwrap();
        assert_eq!(refused.as_deref(), Some("evil.example:5432"));
        assert!(spec.password.is_none() && !spec.ambient_passfile && spec.withheld);

        let (mut spec, flags) = resolved("host=evil.example user=u password=typed dbname=d", &env);
        assert_eq!(
            restrict_to_allowed(&mut spec, &flags, None, &allowed).unwrap(),
            None
        );
        assert_eq!(spec.password.as_ref().map(Secret::expose), Some("typed"));
        assert!(!spec.ambient_passfile);

        let (mut spec, flags) = resolved("host=DB.internal user=u dbname=d", &env);
        assert_eq!(
            restrict_to_allowed(&mut spec, &flags, None, &allowed).unwrap(),
            None
        );
        assert_eq!(
            spec.password.as_ref().map(Secret::expose),
            Some("operator-pw")
        );
        assert!(spec.ambient_passfile);

        let (mut spec, flags) = resolved("host=127.0.0.1 port=5432 user=u dbname=d", &env);
        assert_eq!(
            restrict_to_allowed(&mut spec, &flags, None, &allowed).unwrap(),
            None
        );
        let (mut spec, flags) = resolved("host=127.0.0.1 port=55902 user=u dbname=d", &env);
        assert_eq!(
            restrict_to_allowed(&mut spec, &flags, None, &allowed)
                .unwrap()
                .as_deref(),
            Some("127.0.0.1:55902")
        );
        assert!(spec.password.is_none());

        let (mut spec, flags) = resolved("host=evil.example user=u dbname=d", &env);
        let e = restrict_to_allowed(&mut spec, &flags, Some(&azure()), &allowed).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(e.text().contains("PGMI_SERVE_HOSTS"), "{}", e.text());
    }

    #[test]
    fn allowed_host_entries_are_parsed_and_an_unbracketed_ipv6_is_refused() {
        let ok = |e: &str| AllowedHost::parse(e).unwrap();
        assert_eq!(
            ok("db"),
            AllowedHost {
                host: "db".into(),
                port: None
            }
        );
        assert_eq!(
            ok("db:6432"),
            AllowedHost {
                host: "db".into(),
                port: Some(6432)
            }
        );
        assert_eq!(
            ok("[::1]"),
            AllowedHost {
                host: "::1".into(),
                port: None
            }
        );
        assert_eq!(
            ok("[::1]:5432"),
            AllowedHost {
                host: "::1".into(),
                port: Some(5432)
            }
        );
        for bad in [
            "::1:5432",
            "::1",
            "[::1",
            "[db]:5432",
            "db:0",
            "db:x",
            ":5432",
            "[::1]5432",
        ] {
            let e = AllowedHost::parse(bad).unwrap_err();
            assert_eq!(e.exit_code(), 10, "{bad}");
        }
    }

    #[test]
    fn an_ipv6_entry_matches_its_address_and_port_only() {
        let allowed = ServeHosts::new(Some("[::1]:5432"), None).unwrap();
        let (mut spec, flags) = resolved("postgresql://u@[::1]:5432/d", &[]);
        assert_eq!(
            restrict_to_allowed(&mut spec, &flags, None, &allowed).unwrap(),
            None
        );
        let (mut spec, flags) = resolved("postgresql://u@[0:0:0:0:0:0:0:1]:5432/d", &[]);
        assert_eq!(
            restrict_to_allowed(&mut spec, &flags, None, &allowed).unwrap(),
            None
        );
        let (mut spec, flags) = resolved("postgresql://u@[::1]:5433/d", &[]);
        assert!(
            restrict_to_allowed(&mut spec, &flags, None, &allowed)
                .unwrap()
                .is_some()
        );
        let (own, _) = resolved("postgresql://u@[::1:5432]:5432/d", &[]);
        let allowed = ServeHosts::new(None, Some(&own)).unwrap();
        let (mut spec, flags) = resolved("postgresql://u@[::1]:5432/d", &[]);
        assert!(
            restrict_to_allowed(&mut spec, &flags, None, &allowed)
                .unwrap()
                .is_some()
        );
    }

    #[test]
    fn a_tool_call_cannot_lower_the_operators_settings_for_its_credentials() {
        let (own, _) = resolved(
            "host=db user=u dbname=d sslmode=require channel_binding=require require_auth=scram-sha-256",
            &[],
        );
        let allowed = ServeHosts::new(None, Some(&own)).unwrap();
        let (mut spec, flags) = resolved(
            "host=db user=u dbname=d sslmode=disable channel_binding=disable",
            &[],
        );
        assert_eq!(
            restrict_to_allowed(&mut spec, &flags, None, &allowed).unwrap(),
            None
        );
        assert_eq!(spec.sslmode(), SslMode::Require);
        assert_eq!(spec.channel_binding, ChannelBinding::Require);
        assert_eq!(
            spec.require_auth.policy().unwrap().map(tap::Policy::text),
            Some("scram-sha-256")
        );

        let (mut spec, flags) = resolved("host=db user=u dbname=d sslmode=verify-full", &[]);
        restrict_to_allowed(&mut spec, &flags, None, &allowed).unwrap();
        assert_eq!(spec.sslmode(), SslMode::VerifyFull);

        let (mut spec, flags) = resolved("host=db user=u dbname=d require_auth=password", &[]);
        let e = restrict_to_allowed(&mut spec, &flags, None, &allowed).unwrap_err();
        assert_eq!(e.exit_code(), 10);
        assert!(
            e.text().contains("require_auth \"password\""),
            "{}",
            e.text()
        );

        let (mut spec, flags) =
            resolved("host=db user=u password=own dbname=d sslmode=disable", &[]);
        restrict_to_allowed(&mut spec, &flags, None, &allowed).unwrap();
        assert_eq!(spec.sslmode(), SslMode::Disable);
    }

    #[test]
    fn a_tool_call_cannot_swap_the_root_certificate_the_operators_credentials_are_verified_with() {
        let operator = [("PGSSLMODE", "verify-ca"), ("PGSSLROOTCERT", "/op/ca.crt")];
        let (own, _) = resolved("host=db user=u dbname=d", &operator);
        let allowed = ServeHosts::new(None, Some(&own)).unwrap();
        let refused = |conn: &str, env: &[(&str, &str)], yaml: Option<&str>| {
            let flags = resolve::Flags {
                connection: (!conn.is_empty()).then(|| conn.into()),
                ..resolve::Flags::default()
            };
            let yaml = yaml.map(|y| crate::config::yaml::Connection {
                host: Some("db".into()),
                database: Some("d".into()),
                sslrootcert: Some(y.into()),
                ..Default::default()
            });
            let mut spec = resolve::resolve(&flags, &Env::from_pairs(env), yaml.as_ref()).unwrap();
            restrict_to_allowed(&mut spec, &flags, None, &allowed)
                .err()
                .map(|e| (e.exit_code(), e.text()))
        };
        for conn in [
            "host=db user=u dbname=d sslrootcert=/mitm/ca.crt",
            "host=db user=u dbname=d sslmode=verify-full sslrootcert=/mitm/ca.crt",
            "postgresql://u@db/d?sslrootcert=%2Fmitm%2Fca.crt",
            "postgresql://u@db/d?sslrootcert=system",
            "postgresql://u@db/d?sslrootcert=",
            "Host=db;Username=u;Database=d;Root Certificate=/mitm/ca.crt",
        ] {
            let (code, text) = refused(conn, &[], None).unwrap_or_else(|| panic!("{conn}"));
            assert_eq!(code, 10, "{conn}");
            assert!(
                text.contains("is not the operator's own (\"/op/ca.crt\")"),
                "{conn}: {text}"
            );
        }
        let (_, text) = refused("", &[("PGUSER", "u")], Some("/mitm/ca.crt")).unwrap();
        assert!(
            text.starts_with("sslrootcert \"/mitm/ca.crt\" is not"),
            "{text}"
        );
        let (_, text) = refused("host=db user=u dbname=d", &[], None).unwrap();
        assert!(text.starts_with("sslrootcert unset is not"), "{text}");

        assert_eq!(refused("host=db user=u dbname=d", &operator, None), None);
        assert_eq!(
            refused("host=db user=u dbname=d sslrootcert=/op/ca.crt", &[], None),
            None
        );
        assert_eq!(
            refused(
                "host=db user=u password=own dbname=d sslrootcert=/mitm/ca.crt",
                &operator,
                None
            ),
            None
        );
        assert_eq!(
            refused(
                "host=elsewhere user=u dbname=d sslrootcert=/mitm/ca.crt",
                &operator,
                None
            ),
            None
        );

        let (plain, _) = resolved("host=db user=u dbname=d", &[]);
        let allowed = ServeHosts::new(None, Some(&plain)).unwrap();
        let (mut spec, flags) = resolved("host=db user=u dbname=d sslrootcert=/any/ca.crt", &[]);
        let e = restrict_to_allowed(&mut spec, &flags, None, &allowed).unwrap_err();
        assert!(
            e.text().contains("is not the operator's own (unset)"),
            "{}",
            e.text()
        );
    }
}
