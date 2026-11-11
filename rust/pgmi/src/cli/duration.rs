use std::time::Duration;

pub fn parse(s: &str) -> Result<Duration, String> {
    let bad = || format!("invalid duration {s:?}");
    let mut rest = s;
    let negative = rest.starts_with('-');
    if let Some(r) = rest.strip_prefix(['-', '+']) {
        rest = r;
    }
    if rest == "0" {
        return Ok(Duration::ZERO);
    }
    if rest.is_empty() {
        return Err(bad());
    }
    let mut total = 0f64;
    while !rest.is_empty() {
        let n = rest
            .find(|c: char| !(c.is_ascii_digit() || c == '.'))
            .unwrap_or(rest.len());
        let num = &rest[..n];
        if num.is_empty() || num == "." {
            return Err(bad());
        }
        let value: f64 = num.parse().map_err(|_| bad())?;
        rest = &rest[n..];
        let u = rest
            .find(|c: char| c.is_ascii_digit() || c == '.')
            .unwrap_or(rest.len());
        let scale = match &rest[..u] {
            "ns" => 1e-9,
            "us" | "µs" | "μs" => 1e-6,
            "ms" => 1e-3,
            "s" => 1.0,
            "m" => 60.0,
            "h" => 3600.0,
            _ => return Err(bad()),
        };
        rest = &rest[u..];
        total += value * scale;
    }
    if negative && total > 0.0 {
        return Err(format!("invalid duration {s:?}: must not be negative"));
    }
    if total > MAX_SECS {
        return Err(bad());
    }
    Duration::try_from_secs_f64(total).map_err(|_| bad())
}

const MAX_SECS: f64 = i64::MAX as f64 / 1e9;

pub fn render(d: Duration) -> String {
    if d.is_zero() {
        return "0s".into();
    }
    let nanos = d.as_nanos();
    if nanos < 1_000_000_000 {
        return format!("{:?}", d);
    }
    let secs = d.as_secs();
    let (h, m) = (secs / 3600, secs / 60 % 60);
    let s = (nanos % 60_000_000_000) as f64 / 1e9;
    let mut out = String::new();
    if h > 0 {
        out.push_str(&format!("{h}h"));
    }
    if h > 0 || m > 0 {
        out.push_str(&format!("{m}m"));
    }
    let mut sec = format!("{s:.9}");
    while sec.ends_with('0') {
        sec.pop();
    }
    if sec.ends_with('.') {
        sec.pop();
    }
    out.push_str(&sec);
    out.push('s');
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn longer_than_go_can_hold_is_refused() {
        assert!(parse("4000000000000000h").is_err());
        assert!(parse("2562048h").is_err());
        assert!(parse("2562047h").is_ok());
    }

    #[test]
    fn go_forms() {
        assert_eq!(parse("90s").unwrap(), Duration::from_secs(90));
        assert_eq!(parse("1h30m").unwrap(), Duration::from_secs(5400));
        assert_eq!(parse("1.5h").unwrap(), Duration::from_secs(5400));
        assert_eq!(parse("300ms").unwrap(), Duration::from_millis(300));
        assert_eq!(parse("0").unwrap(), Duration::ZERO);
        assert!(parse("banana").is_err());
        assert!(parse("10").is_err());
        assert!(parse("").is_err());
        assert!(parse("-5s").is_err());
        assert_eq!(render(Duration::from_secs(2)), "2s");
        assert_eq!(render(Duration::from_secs(180)), "3m0s");
        assert_eq!(render(Duration::from_secs(5400)), "1h30m0s");
        assert_eq!(render(Duration::from_millis(1500)), "1.5s");
    }
}
