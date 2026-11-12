use std::path::{Path, PathBuf};

use crate::error::Secret;

/// What a password file holds for one connection.
#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Lookup {
    Found(Secret),
    NotFound,
    TooOpen(PathBuf),
}

impl Lookup {
    /// libpq's warning for a file it refuses to read.
    pub(crate) fn warning(&self) -> Option<String> {
        match self {
            Lookup::TooOpen(path) => Some(format!(
                "WARNING: password file \"{}\" has group or world access; permissions should be u=rw (0600) or less",
                path.display()
            )),
            Lookup::Found(_) | Lookup::NotFound => None,
        }
    }

    pub(crate) fn password(self) -> Option<Secret> {
        match self {
            Lookup::Found(p) => Some(p),
            Lookup::NotFound | Lookup::TooOpen(_) => None,
        }
    }
}

pub(crate) fn lookup(path: &Path, host: &str, port: u16, db: &str, user: &str) -> Lookup {
    let Ok(meta) = std::fs::metadata(path) else {
        return Lookup::NotFound;
    };
    if !meta.is_file() {
        return Lookup::NotFound;
    }
    if group_or_world(&meta) {
        return Lookup::TooOpen(path.to_path_buf());
    }
    let Ok(text) = std::fs::read_to_string(path) else {
        return Lookup::NotFound;
    };
    let host = if host.starts_with('/') {
        "localhost"
    } else {
        host
    };
    find(&text, host, &port.to_string(), db, user)
        .map_or(Lookup::NotFound, |p| Lookup::Found(Secret::from(p)))
}

#[cfg(unix)]
fn group_or_world(meta: &std::fs::Metadata) -> bool {
    use std::os::unix::fs::PermissionsExt;
    meta.permissions().mode() & 0o077 != 0
}

#[cfg(not(unix))]
fn group_or_world(_: &std::fs::Metadata) -> bool {
    false
}

pub(crate) fn find(text: &str, host: &str, port: &str, db: &str, user: &str) -> Option<String> {
    for line in text.lines() {
        let line = line.strip_suffix('\r').unwrap_or(line);
        if line.starts_with('#') || line.trim().is_empty() {
            continue;
        }
        let mut fields = vec![String::new()];
        let mut chars = line.chars();
        while let Some(c) = chars.next() {
            match c {
                '\\' => {
                    if let (Some(n), Some(last)) = (chars.next(), fields.last_mut()) {
                        last.push(n);
                    }
                }
                ':' if fields.len() < 5 => fields.push(String::new()),
                c => {
                    if let Some(last) = fields.last_mut() {
                        last.push(c);
                    }
                }
            }
        }
        if fields.len() != 5 {
            continue;
        }
        let wanted = [host, port, db, user];
        if wanted
            .iter()
            .zip(&fields)
            .all(|(want, got)| got == "*" || got == want)
        {
            return fields.pop();
        }
    }
    None
}

pub(crate) fn escape(s: &str) -> String {
    s.replace('\\', "\\\\").replace(':', "\\:")
}

pub(crate) fn save(
    path: &Path,
    host: &str,
    port: u16,
    dbs: &[&str],
    user: &str,
    password: &str,
) -> crate::error::Result<()> {
    use crate::error::Error;
    if let Some(dir) = path.parent().filter(|d| !d.as_os_str().is_empty()) {
        let mut b = std::fs::DirBuilder::new();
        b.recursive(true);
        #[cfg(unix)]
        std::os::unix::fs::DirBuilderExt::mode(&mut b, 0o700);
        b.create(dir)
            .map_err(|e| Error::general("create .pgpass directory").with_source(e))?;
    }
    let existing = match std::fs::read_to_string(path) {
        Ok(t) => t,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => String::new(),
        Err(e) => {
            return Err(Error::general("failed to read existing .pgpass").with_source(e));
        }
    };
    let prefixes: Vec<String> = dbs
        .iter()
        .map(|db| format!("{}:{port}:{}:{}:", escape(host), escape(db), escape(user)))
        .collect();
    let mut lines: Vec<String> = prefixes
        .iter()
        .map(|p| format!("{p}{}", escape(password)))
        .collect();
    let kept = existing.trim_end_matches('\n');
    lines.extend(
        kept.split('\n')
            .filter(|l| !kept.is_empty() && !prefixes.iter().any(|p| l.starts_with(p.as_str())))
            .map(str::to_string),
    );
    let mut text = lines.join("\n");
    text.push('\n');
    crate::config::edit::write_private(path, &text)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn save_puts_entries_first_and_escapes() {
        let dir = crate::testdir::TempDir::new("pgpass");
        let path = dir.join("sub").join(".pgpass");
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(&path, "*:*:*:u:old\nh\\:x:5432:db:u:stale\n").unwrap();
        save(&path, "h:x", 5432, &["db", "postgres"], "u", "p:w\\d").unwrap();
        let text = std::fs::read_to_string(&path).unwrap();
        assert_eq!(
            text,
            "h\\:x:5432:db:u:p\\:w\\\\d\nh\\:x:5432:postgres:u:p\\:w\\\\d\n*:*:*:u:old\n"
        );
        assert_eq!(
            find(&text, "h:x", "5432", "db", "u").as_deref(),
            Some("p:w\\d")
        );
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                std::fs::metadata(&path).unwrap().permissions().mode() & 0o777,
                0o600
            );
        }
    }

    #[test]
    fn escape_doubles_backslashes_and_escapes_colons() {
        for (input, want) in [
            ("simple", "simple"),
            ("pass:word", "pass\\:word"),
            ("back\\slash", "back\\\\slash"),
            ("both\\:chars", "both\\\\\\:chars"),
            ("", ""),
            ("\\:\\", "\\\\\\:\\\\"),
            ("multi:colon:password", "multi\\:colon\\:password"),
        ] {
            assert_eq!(escape(input), want, "{input}");
        }
    }

    fn saved(existing: Option<&str>, saves: &[(&str, u16, &[&str], &str, &str)]) -> String {
        let dir = crate::testdir::TempDir::new("pgpass-save");
        let path = dir.join("pgpass.conf");
        if let Some(text) = existing {
            std::fs::write(&path, text).unwrap();
        }
        for (host, port, dbs, user, password) in saves {
            save(&path, host, *port, dbs, user, password).unwrap();
        }
        std::fs::read_to_string(&path).unwrap()
    }

    #[test]
    fn save_writes_a_new_file() {
        assert_eq!(
            saved(None, &[("localhost", 5432, &["testdb"], "user", "secret")]),
            "localhost:5432:testdb:user:secret\n"
        );
    }

    #[test]
    fn save_replaces_its_own_line_and_keeps_the_others_after_it() {
        assert_eq!(
            saved(
                Some(
                    "otherhost:5432:otherdb:otheruser:oldpass\nlocalhost:5432:testdb:user:oldpass\n"
                ),
                &[("localhost", 5432, &["testdb"], "user", "newpass")]
            ),
            "localhost:5432:testdb:user:newpass\notherhost:5432:otherdb:otheruser:oldpass\n"
        );
        assert_eq!(
            saved(
                Some("otherhost:5432:otherdb:otheruser:pass\n"),
                &[("newhost", 5433, &["newdb"], "newuser", "newpass")]
            ),
            "newhost:5433:newdb:newuser:newpass\notherhost:5432:otherdb:otheruser:pass\n"
        );
    }

    #[test]
    fn save_outranks_a_matching_wildcard_and_keeps_it() {
        let text = saved(
            Some("# my passwords\n*:*:*:user:stalepass\nother:5432:db:u:p\n"),
            &[("localhost", 5432, &["testdb"], "user", "freshpass")],
        );
        assert_eq!(
            text,
            "localhost:5432:testdb:user:freshpass\n# my passwords\n*:*:*:user:stalepass\nother:5432:db:u:p\n"
        );
        assert_eq!(
            find(&text, "localhost", "5432", "testdb", "user").as_deref(),
            Some("freshpass")
        );
    }

    #[test]
    fn save_never_accumulates_lines() {
        assert_eq!(
            saved(
                None,
                &[
                    ("h", 1, &["d"], "u", "p1"),
                    ("h", 1, &["d"], "u", "p2"),
                    ("h", 1, &["d"], "u", "p3"),
                ]
            ),
            "h:1:d:u:p3\n"
        );
    }

    #[cfg(unix)]
    #[test]
    fn save_leaves_the_file_owner_only_and_no_temp_file() {
        use std::os::unix::fs::PermissionsExt;
        let dir = crate::testdir::TempDir::new("pgpass-mode");
        let path = dir.join("pgpass.conf");
        let tmp = dir.join("pgpass.conf.pgmi-tmp");
        std::fs::write(&tmp, "stale").unwrap();
        std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o644)).unwrap();
        save(&path, "h", 5432, &["d"], "u", "p").unwrap();
        assert_eq!(
            std::fs::metadata(&path).unwrap().permissions().mode() & 0o777,
            0o600
        );
        assert_eq!(std::fs::read_to_string(&tmp).unwrap(), "stale");
        assert_eq!(std::fs::read_dir(dir.path()).unwrap().count(), 2);
    }

    #[test]
    fn lookup_tells_a_found_password_from_a_missing_file_and_a_missing_line() {
        let dir = crate::testdir::TempDir::new("pgpass-lookup");
        let path = dir.join("pgpass.conf");
        assert_eq!(lookup(&path, "h", 5432, "d", "u"), Lookup::NotFound);
        save(&path, "h", 5432, &["d"], "u", "s3cret").unwrap();
        let found = lookup(&path, "h", 5432, "d", "u");
        assert_eq!(found.warning(), None);
        assert_eq!(
            found.password().as_ref().map(Secret::expose),
            Some("s3cret")
        );
        assert_eq!(lookup(&path, "h", 5432, "d", "other"), Lookup::NotFound);
        assert_eq!(
            format!("{:?}", lookup(&path, "h", 5432, "d", "u")),
            "Found([redacted])"
        );
    }

    #[cfg(unix)]
    #[test]
    fn a_file_others_can_read_is_refused_with_libpqs_warning() {
        use std::os::unix::fs::PermissionsExt;
        let dir = crate::testdir::TempDir::new("pgpass-open");
        let path = dir.join("pgpass.conf");
        save(&path, "h", 5432, &["d"], "u", "s3cret").unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644)).unwrap();
        let refused = lookup(&path, "h", 5432, "d", "u");
        assert_eq!(refused, Lookup::TooOpen(path.clone()));
        assert_eq!(
            refused.warning().unwrap(),
            format!(
                "WARNING: password file \"{}\" has group or world access; permissions should be u=rw (0600) or less",
                path.display()
            )
        );
        assert_eq!(refused.password(), None);
    }

    #[test]
    fn matching() {
        let text = "# c\nh:5432:*:u:first\n*:*:*:u:wild\\:card\n";
        assert_eq!(find(text, "h", "5432", "db", "u").as_deref(), Some("first"));
        assert_eq!(
            find(text, "other", "1", "db", "u").as_deref(),
            Some("wild:card")
        );
        assert_eq!(find(text, "other", "1", "db", "x"), None);
    }
}
