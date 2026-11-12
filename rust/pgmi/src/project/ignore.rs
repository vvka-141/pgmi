#[derive(Debug)]
struct Pattern {
    glob: Vec<char>,
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
            let mut p: Vec<char> = line.chars().collect();
            while p.last() == Some(&' ') && !(p.len() >= 2 && p[p.len() - 2] == '\\') {
                p.pop();
            }
            if p.is_empty() || p[0] == '#' {
                continue;
            }
            let mut negated = false;
            if p[0] == '!' {
                negated = true;
                p.remove(0);
            } else if p.starts_with(&['\\', '!']) || p.starts_with(&['\\', '#']) {
                p.remove(0);
            }
            let mut dir_only = false;
            if p.last() == Some(&'/') {
                dir_only = true;
                p.pop();
            }
            if p.is_empty() {
                continue;
            }
            let anchored = p.contains(&'/');
            if p[0] == '/' {
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
            if wildmatch(&p.glob, &subject.chars().collect::<Vec<_>>()) {
                out = !p.negated;
            }
        }
        out
    }
}

fn valid(p: &[char]) -> bool {
    let mut i = 0;
    while i < p.len() {
        match p[i] {
            '\\' => i += 2,
            '[' => {
                let first = i + 1 + usize::from(matches!(p.get(i + 1), Some('!' | '^')));
                if p.get(first) == Some(&']') {
                    return false;
                }
                match class_end(p, i) {
                    Some(e) if class_valid(p, first, e) => i = e,
                    Some(_) => return false,
                    None => i += 1,
                }
            }
            _ => i += 1,
        }
    }
    true
}

fn class_valid(p: &[char], mut i: usize, end: usize) -> bool {
    let last = end - 1;
    let at = |k: usize| p.get(k).copied();
    while i < last {
        if let Some((name, next)) = named(p, i) {
            if posix(name, 'a').is_none() {
                return false;
            }
            i = next;
            continue;
        }
        if at(i) == Some('\\') {
            i += 1;
        }
        let Some(lo) = at(i) else { return false };
        if at(i + 1) == Some('-') && i + 2 < last {
            let (hi, step) = match at(i + 2) {
                Some('\\') => (at(i + 3), 4),
                h => (h, 3),
            };
            if hi.is_none_or(|hi| lo > hi) {
                return false;
            }
            i += step;
        } else {
            i += 1;
        }
    }
    true
}

fn class_end(p: &[char], open: usize) -> Option<usize> {
    let mut i = open + 1;
    if matches!(p.get(i), Some('!' | '^')) {
        i += 1;
    }
    if p.get(i) == Some(&']') {
        i += 1;
    }
    while i < p.len() {
        match p[i] {
            '\\' => i += 2,
            '[' if named(p, i).is_some() => i = named(p, i)?.1,
            ']' => return Some(i + 1),
            _ => i += 1,
        }
    }
    None
}

/// A `[:name:]` at `i`, as git reads it: the next `]` closes it only when
/// a `:` comes right before; otherwise the `[` is an ordinary member.
fn named(p: &[char], i: usize) -> Option<(&[char], usize)> {
    if p.get(i) != Some(&'[') || p.get(i + 1) != Some(&':') {
        return None;
    }
    let close = i + 2 + p[i + 2..].iter().position(|c| *c == ']')?;
    (close > i + 2 && p[close - 1] == ':').then(|| (&p[i + 2..close - 1], close + 1))
}

fn posix(name: &[char], c: char) -> Option<bool> {
    Some(match name.iter().collect::<String>().as_str() {
        "alnum" => c.is_ascii_alphanumeric(),
        "alpha" => c.is_ascii_alphabetic(),
        "blank" => c == ' ' || c == '\t',
        "cntrl" => c.is_ascii_control(),
        "digit" => c.is_ascii_digit(),
        "graph" => c.is_ascii_graphic(),
        "lower" => c.is_ascii_lowercase(),
        "print" => c.is_ascii_graphic() || c == ' ',
        "punct" => c.is_ascii_punctuation(),
        "space" => matches!(c, ' ' | '\t' | '\n' | '\r' | '\u{b}' | '\u{c}'),
        "upper" => c.is_ascii_uppercase(),
        "xdigit" => c.is_ascii_hexdigit(),
        _ => return None,
    })
}

fn class_match(p: &[char], open: usize, c: char) -> Option<(bool, usize)> {
    let end = class_end(p, open)?;
    let mut i = open + 1;
    let negated = matches!(p.get(i), Some('!' | '^'));
    if negated {
        i += 1;
    }
    let mut hit = false;
    let mut first = true;
    while i < end - 1 {
        let mut lo = p[i];
        if lo == ']' && !first {
            break;
        }
        first = false;
        if let Some((name, next)) = named(p, i) {
            hit |= posix(name, c)?;
            i = next;
            continue;
        }
        if lo == '\\' {
            i += 1;
            lo = *p.get(i)?;
        }
        if p.get(i + 1) == Some(&'-') && i + 2 < end - 1 {
            let mut hi = p[i + 2];
            let mut step = 3;
            if hi == '\\' {
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
    Some((hit != negated && c != '/', end))
}

pub(crate) fn wildmatch(p: &[char], t: &[char]) -> bool {
    matches(p, 0, t, 0) == Match::Yes
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Match {
    Yes,
    No,
    AbortAll,
    AbortToStarStar,
}

fn matches(p: &[char], mut pi: usize, t: &[char], mut ti: usize) -> Match {
    while pi < p.len() {
        if ti >= t.len() && p[pi] != '*' {
            return Match::AbortAll;
        }
        match p[pi] {
            '*' => {
                let double = p.get(pi + 1) == Some(&'*');
                if double
                    && (pi == 0 || p[pi - 1] == '/')
                    && (pi + 2 == p.len() || p[pi + 2] == '/')
                {
                    if pi + 2 == p.len() {
                        return Match::Yes;
                    }
                    let rest = pi + 3;
                    let starts = std::iter::once(ti)
                        .chain((ti..t.len()).filter(|k| t[*k] == '/').map(|k| k + 1));
                    for k in starts {
                        if let r @ (Match::Yes | Match::AbortAll) = matches(p, rest, t, k) {
                            return r;
                        }
                    }
                    return Match::No;
                }
                while pi < p.len() && p[pi] == '*' {
                    pi += 1;
                }
                if pi == p.len() {
                    return if t[ti..].contains(&'/') {
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
                    if t[k] == '/' {
                        return Match::AbortToStarStar;
                    }
                }
                return Match::AbortAll;
            }
            '?' => {
                if t[ti] == '/' {
                    return Match::No;
                }
                pi += 1;
                ti += 1;
            }
            '[' => match class_match(p, pi, t[ti]) {
                Some((true, end)) => {
                    pi = end;
                    ti += 1;
                }
                None if class_end(p, pi).is_none() && t[ti] == '[' => {
                    pi += 1;
                    ti += 1;
                }
                _ => return Match::No,
            },
            '\\' if pi + 1 < p.len() => {
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

    fn wm(p: &str, t: &str) -> bool {
        wildmatch(
            &p.chars().collect::<Vec<_>>(),
            &t.chars().collect::<Vec<_>>(),
        )
    }

    #[test]
    fn globs() {
        assert!(wm("*.bak", "b.bak"));
        assert!(!wm("*.sql", "a/b.sql"));
        assert!(wm("[Bb]uild", "Build"));
        assert!(wm("**/scratch", "sub/scratch"));
        assert!(wm("**/scratch", "scratch"));
        assert!(wm("docs/**", "docs/drop.sql"));
        assert!(!wm("docs/**", "docs"));
        assert!(wm("a/**/b", "a/b"));
        assert!(wm("a/**/b", "a/x/y/b"));
        assert!(wm("[!a-m]*.tmp", "n.tmp"));
        assert!(!wm("[!a-m]*.tmp", "c.tmp"));
        assert!(wm("odd[&&]name.sql", "odd&name.sql"));
        assert!(!wm("odd[&&]name.sql", "oddname.sql"));
        assert!(wm("dash[--]x.sql", "dash-x.sql"));
        assert!(wm("\\#hash.sql", "#hash.sql"));
        assert!(wm("sp[[:space:]]ace.sql", "sp ace.sql"));
        assert!(wm("* (copy)*", "a (copy).sql"));
        assert!(wm("*~", "a.sql~"));
    }

    #[test]
    fn many_stars_do_not_backtrack_exponentially() {
        let text = "a".repeat(60);
        let started = std::time::Instant::now();
        assert!(!wm("*a*a*a*a*a*a*a*a*b", &text));
        assert!(!wm("**/*a*a*a*a*a*a*a*a*b", &text));
        assert!(wm("*a*a*a*a*a*a*a*a*a", &text));
        assert!(started.elapsed() < std::time::Duration::from_secs(1));
    }

    fn naive(p: &[char], pi: usize, t: &[char], ti: usize) -> bool {
        let Some(&c) = p.get(pi) else {
            return ti == t.len();
        };
        match c {
            '*' if p.get(pi + 1) == Some(&'*')
                && (pi == 0 || p[pi - 1] == '/')
                && (pi + 2 == p.len() || p[pi + 2] == '/') =>
            {
                pi + 2 == p.len()
                    || naive(p, pi + 3, t, ti)
                    || (ti..t.len()).any(|k| t[k] == '/' && naive(p, pi + 3, t, k + 1))
            }
            '*' => {
                let mut q = pi;
                while p.get(q) == Some(&'*') {
                    q += 1;
                }
                (ti..=t.len())
                    .take_while(|k| *k == ti || t[k - 1] != '/')
                    .any(|k| naive(p, q, t, k))
            }
            '?' => t.get(ti).is_some_and(|x| *x != '/') && naive(p, pi + 1, t, ti + 1),
            c => t.get(ti) == Some(&c) && naive(p, pi + 1, t, ti + 1),
        }
    }

    proptest::proptest! {
        #[test]
        fn abort_rule_keeps_the_answer(
            p in proptest::collection::vec(proptest::sample::select(vec!['a', 'é', '/', '*', '?']), 0..10),
            t in proptest::collection::vec(proptest::sample::select(vec!['a', 'é', '/']), 0..10),
        ) {
            proptest::prop_assert_eq!(wildmatch(&p, &t), naive(&p, 0, &t, 0));
        }
    }

    const NAMES: &[&str] = &[
        "café.sql",
        "cafe.sql",
        "e/ä.sql",
        "e/日本.sql",
        "e/ab.sql",
        "x1.sql",
        "xx.sql",
        "x:.sql",
        "a",
        "[",
        ":",
        "b",
        "sp ace.sql",
        "odd&name.sql",
        "oddname.sql",
        "dash-x.sql",
        "#hash.sql",
        "[abc",
        "a\\",
        "-",
        "ax",
        "zx",
        "n.tmp",
        "c.tmp",
        "b.bak",
        "sub/scratch",
        "scratch",
        "docs/drop.sql",
        "docs",
        "a/b",
        "a/x/y/b",
        "a (copy).sql",
        "a.sql~",
        "q]r.sql",
        "x[1].sql",
        "abc",
        "a/bc",
        "~x",
        "~",
    ];

    #[test]
    fn patterns_match_and_refuse_as_go_does() {
        let table: &[(&str, Option<&[&str]>)] = &[
            ("caf?.sql", Some(&["café.sql", "cafe.sql"][..])),
            ("caf[é].sql", Some(&["café.sql"][..])),
            ("caf[^e].sql", Some(&["café.sql"][..])),
            ("caf[!e].sql", Some(&["café.sql"][..])),
            ("e/?.sql", Some(&["e/ä.sql"][..])),
            ("e/??.sql", Some(&["e/日本.sql", "e/ab.sql"][..])),
            ("e/[ä].sql", Some(&["e/ä.sql"][..])),
            ("e/日?.sql", Some(&["e/日本.sql"][..])),
            ("[a[:b]", Some(&["a", "[", ":", "b", "a/b", "a/x/y/b"][..])),
            ("odd[&&]name.sql", Some(&["odd&name.sql"][..])),
            ("dash[--]x.sql", Some(&["dash-x.sql"][..])),
            ("\\#hash.sql", Some(&["#hash.sql"][..])),
            ("[z-a]", None),
            ("[]", None),
            ("[!]", None),
            ("[^]", None),
            ("[]a]", None),
            ("a[]b", None),
            ("[[:nosuch:]]", None),
            ("[abc", Some(&["[abc"][..])),
            ("[a-]", Some(&["a", "-"][..])),
            ("[a-a]", Some(&["a"][..])),
            ("[!a-z]x", Some(&["~x"][..])),
            ("[!a-m]*.tmp", Some(&["n.tmp"][..])),
            ("*.bak", Some(&["b.bak"][..])),
            ("**/scratch", Some(&["sub/scratch", "scratch"][..])),
            ("docs/**", Some(&["docs/drop.sql"][..])),
            ("a/**/b", Some(&["a/b", "a/x/y/b"][..])),
            ("* (copy)*", Some(&["a (copy).sql"][..])),
            ("*~", Some(&["a.sql~", "~"][..])),
            ("q]r.sql", Some(&["q]r.sql"][..])),
            ("x[1].sql", Some(&["x1.sql"][..])),
            (
                "a**",
                Some(
                    &[
                        "e/ab.sql",
                        "a",
                        "a\\",
                        "ax",
                        "a (copy).sql",
                        "a.sql~",
                        "abc",
                    ][..],
                ),
            ),
            ("[~]x", Some(&["~x"][..])),
            ("[a~~b]", Some(&["a", "b", "a/b", "a/x/y/b", "~"][..])),
        ];
        for (pattern, want) in table {
            let got = Rules::parse(pattern).ok().map(|r| {
                NAMES
                    .iter()
                    .copied()
                    .filter(|n| r.ignored(n, false))
                    .collect::<Vec<_>>()
            });
            assert_eq!(got.as_deref(), *want, "{pattern}");
        }
    }

    #[test]
    fn posix_classes_keep_git_meaning_where_go_refuses_them() {
        let r = Rules::parse("x[[:digit:]].sql\nsp[[:space:]]ace.sql\n").unwrap();
        assert!(r.ignored("x1.sql", false));
        assert!(!r.ignored("xx.sql", false));
        assert!(r.ignored("sp ace.sql", false));
        assert!(!r.ignored("spxace.sql", false));
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
        assert_eq!(
            Rules::parse("ok\na[z-a]\n").err(),
            Some(".pgmiignore line 2: invalid pattern \"a[z-a]\"".to_string())
        );
    }

    #[test]
    fn broken_patterns_are_refused_as_go_refuses_them() {
        for bad in [
            "[z-a]",
            "x[b-a].sql",
            "[]",
            "[!]",
            "[^]",
            "[]a]",
            "a[]b",
            "[[:nosuch:]]",
        ] {
            assert!(Rules::parse(bad).is_err(), "{bad} accepted");
        }
        for ok in ["[abc", "a[\\]", "[a-]", "[a-a]", "[[:digit:]]", "[!a-z]x"] {
            assert!(Rules::parse(ok).is_ok(), "{ok} refused");
        }
    }

    #[test]
    fn an_unclosed_bracket_is_a_literal_bracket() {
        let r = Rules::parse("[abc\n").unwrap();
        assert!(r.ignored("[abc", false));
        assert!(!r.ignored("a", false));
        assert!(!r.ignored("abc", false));
    }
}
