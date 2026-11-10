use std::path::{Path, PathBuf};

pub struct Lookup {
    pub password: Option<String>,
    pub warning: Option<String>,
}

pub fn default_path(home: Option<&Path>) -> Option<PathBuf> {
    home.map(|h| h.join(".pgpass"))
}

pub fn lookup(path: &Path, host: &str, port: u16, db: &str, user: &str) -> Lookup {
    let none = Lookup {
        password: None,
        warning: None,
    };
    let Ok(meta) = std::fs::metadata(path) else {
        return none;
    };
    if !meta.is_file() {
        return none;
    }
    if group_or_world(&meta) {
        return Lookup {
            password: None,
            warning: Some(format!(
                "WARNING: password file \"{}\" has group or world access; permissions should be u=rw (0600) or less",
                path.display()
            )),
        };
    }
    let Ok(text) = std::fs::read_to_string(path) else {
        return none;
    };
    let host = if host.starts_with('/') {
        "localhost"
    } else {
        host
    };
    Lookup {
        password: find(&text, host, &port.to_string(), db, user),
        warning: None,
    }
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

fn find(text: &str, host: &str, port: &str, db: &str, user: &str) -> Option<String> {
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

#[cfg(test)]
mod tests {
    use super::*;

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
