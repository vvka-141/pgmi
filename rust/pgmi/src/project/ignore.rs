#[derive(Debug)]
struct Pattern {
    glob: Vec<u8>,
    negated: bool,
    dir_only: bool,
    anchored: bool,
}

#[derive(Debug, Default)]
pub(crate) struct Rules {
    patterns: Vec<Pattern>,
}

impl Rules {
    pub(crate) fn parse(text: &str) -> Result<Self, String> {
        let mut patterns = Vec::new();
        for (n, line) in text.lines().enumerate() {
            let line = line.strip_suffix('\r').unwrap_or(line);
            let mut p = line.as_bytes().to_vec();
            while p.last() == Some(&b' ') && !(p.len() >= 2 && p[p.len() - 2] == b'\\') {
                p.pop();
            }
            if p.is_empty() || p[0] == b'#' {
                continue;
            }
            let mut negated = false;
            if p[0] == b'!' {
                negated = true;
                p.remove(0);
            } else if p.starts_with(b"\\!") || p.starts_with(b"\\#") {
                p.remove(0);
            }
            let mut dir_only = false;
            if p.last() == Some(&b'/') {
                dir_only = true;
                p.pop();
            }
            if p.is_empty() {
                continue;
            }
            let anchored = p.contains(&b'/');
            if p[0] == b'/' {
                p.remove(0);
            }
            if !valid(&p) {
                return Err(format!(
                    ".pgmiignore line {}: invalid pattern {line:?}",
                    n + 1
                ));
            }
            patterns.push(Pattern {
                glob: p,
                negated,
                dir_only,
                anchored,
            });
        }
        Ok(Rules { patterns })
    }

    pub(crate) fn ignored(&self, path: &str, is_dir: bool) -> bool {
        let base = path.rsplit('/').next().unwrap_or(path);
        let mut out = false;
        for p in &self.patterns {
            if p.dir_only && !is_dir {
                continue;
            }
            let subject = if p.anchored { path } else { base };
            if wildmatch(&p.glob, subject.as_bytes()) {
                out = !p.negated;
            }
        }
        out
    }
}

fn valid(p: &[u8]) -> bool {
    let mut i = 0;
    while i < p.len() {
        match p[i] {
            b'\\' => i += 2,
            b'[' => match class_end(p, i) {
                Some(e) => i = e,
                None => return false,
            },
            _ => i += 1,
        }
    }
    true
}

fn class_end(p: &[u8], open: usize) -> Option<usize> {
    let mut i = open + 1;
    if matches!(p.get(i), Some(b'!' | b'^')) {
        i += 1;
    }
    if p.get(i) == Some(&b']') {
        i += 1;
    }
    while i < p.len() {
        match p[i] {
            b'\\' => i += 2,
            b'[' if p.get(i + 1) == Some(&b':') => {
                let close = p[i + 2..].windows(2).position(|w| w == b":]")?;
                i += 2 + close + 2;
            }
            b']' => return Some(i + 1),
            _ => i += 1,
        }
    }
    None
}

fn posix(name: &[u8], c: u8) -> Option<bool> {
    Some(match name {
        b"alnum" => c.is_ascii_alphanumeric(),
        b"alpha" => c.is_ascii_alphabetic(),
        b"blank" => c == b' ' || c == b'\t',
        b"cntrl" => c.is_ascii_control(),
        b"digit" => c.is_ascii_digit(),
        b"graph" => c.is_ascii_graphic(),
        b"lower" => c.is_ascii_lowercase(),
        b"print" => c.is_ascii_graphic() || c == b' ',
        b"punct" => c.is_ascii_punctuation(),
        b"space" => matches!(c, b' ' | b'\t' | b'\n' | b'\r' | 0x0b | 0x0c),
        b"upper" => c.is_ascii_uppercase(),
        b"xdigit" => c.is_ascii_hexdigit(),
        _ => return None,
    })
}

fn class_match(p: &[u8], open: usize, c: u8) -> Option<(bool, usize)> {
    let end = class_end(p, open)?;
    let mut i = open + 1;
    let negated = matches!(p.get(i), Some(b'!' | b'^'));
    if negated {
        i += 1;
    }
    let mut hit = false;
    let mut first = true;
    while i < end - 1 {
        let mut lo = p[i];
        if lo == b']' && !first {
            break;
        }
        first = false;
        if lo == b'[' && p.get(i + 1) == Some(&b':') {
            let close = p[i + 2..].windows(2).position(|w| w == b":]")?;
            let name = &p[i + 2..i + 2 + close];
            hit |= posix(name, c)?;
            i += 2 + close + 2;
            continue;
        }
        if lo == b'\\' {
            i += 1;
            lo = *p.get(i)?;
        }
        if p.get(i + 1) == Some(&b'-') && i + 2 < end - 1 {
            let mut hi = p[i + 2];
            let mut step = 3;
            if hi == b'\\' {
                hi = *p.get(i + 3)?;
                step = 4;
            }
            hit |= lo <= c && c <= hi;
            i += step;
        } else {
            hit |= lo == c;
            i += 1;
        }
    }
    Some((hit != negated && c != b'/', end))
}

pub(crate) fn wildmatch(p: &[u8], t: &[u8]) -> bool {
    matches(p, 0, t, 0) == Match::Yes
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Match {
    Yes,
    No,
    AbortAll,
    AbortToStarStar,
}

fn matches(p: &[u8], mut pi: usize, t: &[u8], mut ti: usize) -> Match {
    while pi < p.len() {
        if ti >= t.len() && p[pi] != b'*' {
            return Match::AbortAll;
        }
        match p[pi] {
            b'*' => {
                let double = p.get(pi + 1) == Some(&b'*');
                if double
                    && (pi == 0 || p[pi - 1] == b'/')
                    && (pi + 2 == p.len() || p[pi + 2] == b'/')
                {
                    if pi + 2 == p.len() {
                        return Match::Yes;
                    }
                    let rest = pi + 3;
                    let starts = std::iter::once(ti)
                        .chain((ti..t.len()).filter(|k| t[*k] == b'/').map(|k| k + 1));
                    for k in starts {
                        if let r @ (Match::Yes | Match::AbortAll) = matches(p, rest, t, k) {
                            return r;
                        }
                    }
                    return Match::No;
                }
                while pi < p.len() && p[pi] == b'*' {
                    pi += 1;
                }
                if pi == p.len() {
                    return if t[ti..].contains(&b'/') {
                        Match::No
                    } else {
                        Match::Yes
                    };
                }
                for k in ti..t.len() {
                    match matches(p, pi, t, k) {
                        Match::No => {}
                        r => return r,
                    }
                    if t[k] == b'/' {
                        return Match::AbortToStarStar;
                    }
                }
                return Match::AbortAll;
            }
            b'?' => {
                if t[ti] == b'/' {
                    return Match::No;
                }
                pi += 1;
                ti += 1;
            }
            b'[' => match class_match(p, pi, t[ti]) {
                Some((true, end)) => {
                    pi = end;
                    ti += 1;
                }
                _ => return Match::No,
            },
            b'\\' if pi + 1 < p.len() => {
                if t[ti] != p[pi + 1] {
                    return Match::No;
                }
                pi += 2;
                ti += 1;
            }
            c => {
                if t[ti] != c {
                    return Match::No;
                }
                pi += 1;
                ti += 1;
            }
        }
    }
    if ti == t.len() { Match::Yes } else { Match::No }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn globs() {
        assert!(wildmatch(b"*.bak", b"b.bak"));
        assert!(!wildmatch(b"*.sql", b"a/b.sql"));
        assert!(wildmatch(b"[Bb]uild", b"Build"));
        assert!(wildmatch(b"**/scratch", b"sub/scratch"));
        assert!(wildmatch(b"**/scratch", b"scratch"));
        assert!(wildmatch(b"docs/**", b"docs/drop.sql"));
        assert!(!wildmatch(b"docs/**", b"docs"));
        assert!(wildmatch(b"a/**/b", b"a/b"));
        assert!(wildmatch(b"a/**/b", b"a/x/y/b"));
        assert!(wildmatch(b"[!a-m]*.tmp", b"n.tmp"));
        assert!(!wildmatch(b"[!a-m]*.tmp", b"c.tmp"));
        assert!(wildmatch(b"odd[&&]name.sql", b"odd&name.sql"));
        assert!(!wildmatch(b"odd[&&]name.sql", b"oddname.sql"));
        assert!(wildmatch(b"dash[--]x.sql", b"dash-x.sql"));
        assert!(wildmatch(b"\\#hash.sql", b"#hash.sql"));
        assert!(wildmatch(b"sp[[:space:]]ace.sql", b"sp ace.sql"));
        assert!(wildmatch(b"* (copy)*", b"a (copy).sql"));
        assert!(wildmatch(b"*~", b"a.sql~"));
    }

    #[test]
    fn many_stars_do_not_backtrack_exponentially() {
        let text = vec![b'a'; 60];
        let started = std::time::Instant::now();
        assert!(!wildmatch(b"*a*a*a*a*a*a*a*a*b", &text));
        assert!(!wildmatch(b"**/*a*a*a*a*a*a*a*a*b", &text));
        assert!(wildmatch(b"*a*a*a*a*a*a*a*a*a", &text));
        assert!(started.elapsed() < std::time::Duration::from_secs(1));
    }

    fn naive(p: &[u8], pi: usize, t: &[u8], ti: usize) -> bool {
        let Some(&c) = p.get(pi) else {
            return ti == t.len();
        };
        match c {
            b'*' if p.get(pi + 1) == Some(&b'*')
                && (pi == 0 || p[pi - 1] == b'/')
                && (pi + 2 == p.len() || p[pi + 2] == b'/') =>
            {
                pi + 2 == p.len()
                    || naive(p, pi + 3, t, ti)
                    || (ti..t.len()).any(|k| t[k] == b'/' && naive(p, pi + 3, t, k + 1))
            }
            b'*' => {
                let mut q = pi;
                while p.get(q) == Some(&b'*') {
                    q += 1;
                }
                (ti..=t.len())
                    .take_while(|k| *k == ti || t[k - 1] != b'/')
                    .any(|k| naive(p, q, t, k))
            }
            b'?' => t.get(ti).is_some_and(|x| *x != b'/') && naive(p, pi + 1, t, ti + 1),
            c => t.get(ti) == Some(&c) && naive(p, pi + 1, t, ti + 1),
        }
    }

    proptest::proptest! {
        #[test]
        fn abort_rule_keeps_the_answer(
            p in proptest::collection::vec(proptest::sample::select(b"ab/*?".to_vec()), 0..10),
            t in proptest::collection::vec(proptest::sample::select(b"ab/".to_vec()), 0..10),
        ) {
            proptest::prop_assert_eq!(wildmatch(&p, &t), naive(&p, 0, &t, 0));
        }
    }

    #[test]
    fn rules() {
        let r =
            Rules::parse("# c\n*.bak\n[Bb]uild/\ndocs/**\n!docs/keep.sql\n\\#hash.sql\n").unwrap();
        assert!(r.ignored("b.bak", false));
        assert!(r.ignored("Build", true));
        assert!(!r.ignored("Build", false));
        assert!(r.ignored("docs/drop.sql", false));
        assert!(!r.ignored("docs/keep.sql", false));
        assert!(r.ignored("#hash.sql", false));
        assert!(Rules::parse("a[b\n").is_err());
    }
}
