use std::collections::VecDeque;
use std::fmt::Write as _;
use std::io::{IsTerminal, Write};
use std::sync::{Arc, Mutex};
use std::time::Instant;

use serde_json::{Map, Value, json};

use crate::env::Env;

/// The severity of a server message; `Other` keeps a name pgmi does not
/// treat specially (`ERROR`, a localized word) as the server sent it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Severity {
    Debug,
    Log,
    Info,
    Notice,
    Warning,
    Other(String),
}

impl Severity {
    pub(crate) fn as_str(&self) -> &str {
        match self {
            Severity::Debug => "DEBUG",
            Severity::Log => "LOG",
            Severity::Info => "INFO",
            Severity::Notice => "NOTICE",
            Severity::Warning => "WARNING",
            Severity::Other(s) => s,
        }
    }
}

impl From<&str> for Severity {
    fn from(s: &str) -> Self {
        match s {
            "DEBUG" => Severity::Debug,
            "LOG" => Severity::Log,
            "INFO" => Severity::Info,
            "NOTICE" => Severity::Notice,
            "WARNING" => Severity::Warning,
            other => Severity::Other(other.to_string()),
        }
    }
}

/// A server message: NOTICE, WARNING, DEBUG and the like.
#[derive(Debug, Clone)]
pub(crate) struct Notice {
    pub(crate) severity: Severity,
    pub(crate) message: String,
    pub(crate) detail: Option<String>,
    pub(crate) hint: Option<String>,
}

impl Notice {
    fn json(&self) -> Value {
        let mut v = Map::new();
        v.insert("severity".into(), json!(self.severity.as_str()));
        v.insert("message".into(), json!(self.message));
        if let Some(d) = &self.detail {
            v.insert("detail".into(), json!(d));
        }
        if let Some(h) = &self.hint {
            v.insert("hint".into(), json!(h));
        }
        Value::Object(v)
    }
}

/// Writes pgmi's own lines to one sink (stderr in production), one whole
/// line at a time even when several deploys share it.
pub(crate) struct Reporter {
    verbose: bool,
    start: Instant,
    fancy: bool,
    color: bool,
    sink: Mutex<Box<dyn Write + Send>>,
}

impl Reporter {
    pub(crate) fn new(verbose: bool, env: &Env) -> Arc<Self> {
        let term =
            std::io::stderr().is_terminal() && env.get("TERM").ok().flatten() != Some("dumb");
        let no_color = env.non_empty("NO_COLOR").ok().flatten().is_some();
        Arc::new(Self::to(
            Box::new(std::io::stderr()),
            verbose,
            term,
            term && !no_color,
        ))
    }

    pub(crate) fn to(sink: Box<dyn Write + Send>, verbose: bool, fancy: bool, color: bool) -> Self {
        Reporter {
            verbose,
            start: Instant::now(),
            fancy,
            color,
            sink: Mutex::new(sink),
        }
    }

    pub(crate) fn line(&self, text: &str) {
        let mut sink = self
            .sink
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        let mut out = String::new();
        if self.verbose {
            let t = self.start.elapsed().as_secs_f64();
            for l in text.lines() {
                let _ = writeln!(out, "[{t:.2}s] {l}");
            }
        } else {
            out.push_str(text);
            out.push('\n');
        }
        let _ = sink.write_all(out.as_bytes());
        let _ = sink.flush();
    }

    /// A `--verbose` line as Go prints it: `[VERBOSE] ` and no elapsed time.
    pub(crate) fn verbose(&self, text: &str) {
        let mut sink = self
            .sink
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        let _ = writeln!(sink, "[VERBOSE] {text}");
        let _ = sink.flush();
    }

    pub(crate) fn notice(&self, n: &Notice) {
        let mut text = match &n.severity {
            Severity::Log => return,
            Severity::Debug if !self.verbose => return,
            Severity::Notice | Severity::Info | Severity::Debug => n.message.clone(),
            s => format!("{}:  {}", s.as_str(), n.message),
        };
        if let Some(d) = &n.detail {
            let _ = write!(text, "\nDETAIL:  {d}");
        }
        if let Some(h) = &n.hint {
            let _ = write!(text, "\nHINT:  {h}");
        }
        self.line(&text);
    }

    pub(crate) fn summary(&self, summary: &Summary) {
        let ok = matches!(summary, Summary::Done(_));
        let (word, open, close) = match (ok, self.fancy, self.color) {
            (true, true, true) => ("✓", "\x1b[32m", "\x1b[0m"),
            (true, true, false) => ("✓", "", ""),
            (true, false, _) => ("ok", "", ""),
            (false, true, true) => ("✗", "\x1b[31m", "\x1b[0m"),
            (false, true, false) => ("✗", "", ""),
            (false, false, _) => ("FAILED", "", ""),
        };
        self.line(&format!("{open}{word}{close} {}", summary.text()));
    }
}

/// The last line of a deploy: what it did, or how far it got.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Summary {
    Done(String),
    Failed(String),
}

impl Summary {
    pub(crate) fn text(&self) -> &str {
        match self {
            Summary::Done(t) | Summary::Failed(t) => t,
        }
    }
}

#[derive(Default)]
pub(crate) struct NoticeLog {
    head: Vec<Notice>,
    warnings: Vec<Notice>,
    tail: VecDeque<Notice>,
    dropped: u64,
    pub(crate) total: u64,
    pub(crate) warning_total: u64,
}

const HEAD: usize = 50;
const WARNINGS: usize = 200;
const TAIL: usize = 150;

impl NoticeLog {
    pub(crate) fn push(&mut self, n: &Notice) {
        if n.severity == Severity::Log {
            return;
        }
        self.total += 1;
        if n.severity == Severity::Warning {
            self.warning_total += 1;
        }
        if self.head.len() < HEAD {
            self.head.push(n.clone());
            return;
        }
        self.tail.push_back(n.clone());
        if self.tail.len() > TAIL
            && let Some(old) = self.tail.pop_front()
        {
            if old.severity == Severity::Warning && self.warnings.len() < WARNINGS {
                self.warnings.push(old);
            } else {
                self.dropped += 1;
            }
        }
    }

    pub(crate) fn finish(&self) -> (Value, Option<u64>) {
        let all: Vec<Value> = self
            .head
            .iter()
            .chain(&self.warnings)
            .chain(&self.tail)
            .map(Notice::json)
            .collect();
        (
            Value::Array(all),
            (self.dropped > 0).then_some(self.dropped),
        )
    }
}

pub(crate) fn seconds(d: std::time::Duration) -> String {
    format!("{:.2}s", d.as_secs_f64())
}

pub(crate) fn timestamp(t: std::time::SystemTime) -> String {
    match jiff::Timestamp::try_from(t) {
        Ok(ts) => ts.strftime("%Y-%m-%dT%H:%M:%S%.3fZ").to_string(),
        Err(_) => String::new(),
    }
}

/// A `Reporter` sink that keeps what was written, for tests.
#[cfg(test)]
#[derive(Clone, Default)]
pub(crate) struct Captured(Arc<Mutex<Vec<u8>>>);

#[cfg(test)]
impl Captured {
    pub(crate) fn reporter(verbose: bool, fancy: bool, color: bool) -> (Reporter, Captured) {
        let out = Captured::default();
        (
            Reporter::to(Box::new(out.clone()), verbose, fancy, color),
            out,
        )
    }

    pub(crate) fn text(&self) -> String {
        String::from_utf8(self.0.lock().unwrap().clone()).unwrap()
    }
}

#[cfg(test)]
impl Write for Captured {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        self.0.lock().unwrap().extend_from_slice(buf);
        Ok(buf.len())
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn notice(sev: &str, detail: Option<&str>, hint: Option<&str>) -> Notice {
        Notice {
            severity: Severity::from(sev),
            message: "msg".into(),
            detail: detail.map(str::to_string),
            hint: hint.map(str::to_string),
        }
    }

    #[test]
    fn the_summary_glyph_follows_the_terminal() {
        for (fancy, color, ok, want) in [
            (false, false, true, "ok app: done\n"),
            (false, false, false, "FAILED app: done\n"),
            (true, false, true, "✓ app: done\n"),
            (true, false, false, "✗ app: done\n"),
            (true, true, true, "\x1b[32m✓\x1b[0m app: done\n"),
            (true, true, false, "\x1b[31m✗\x1b[0m app: done\n"),
        ] {
            let (r, out) = Captured::reporter(false, fancy, color);
            let summary = if ok {
                Summary::Done("app: done".into())
            } else {
                Summary::Failed("app: done".into())
            };
            r.summary(&summary);
            assert_eq!(out.text(), want, "fancy={fancy} color={color} ok={ok}");
        }
    }

    #[test]
    fn verbose_prefixes_every_line_with_the_elapsed_time() {
        let (r, out) = Captured::reporter(true, false, false);
        r.line("one\ntwo");
        let text = out.text();
        let lines: Vec<&str> = text.lines().collect();
        assert_eq!(lines.len(), 2, "{text}");
        for (line, body) in lines.iter().zip(["one", "two"]) {
            let (stamp, rest) = line.split_once("] ").unwrap();
            assert_eq!(rest, body);
            let secs = stamp.strip_prefix('[').unwrap().strip_suffix('s').unwrap();
            assert_eq!(secs.split_once('.').unwrap().1.len(), 2, "{line}");
        }
        let (r, out) = Captured::reporter(false, false, false);
        r.line("one\ntwo");
        assert_eq!(out.text(), "one\ntwo\n");
    }

    #[test]
    fn notices_are_laid_out_as_psql_does() {
        let (r, out) = Captured::reporter(false, false, false);
        r.notice(&notice("NOTICE", None, None));
        r.notice(&notice("INFO", None, None));
        r.notice(&notice("WARNING", Some("d"), Some("h")));
        r.notice(&notice("LOG", None, None));
        r.notice(&notice("DEBUG", None, None));
        assert_eq!(
            out.text(),
            "msg\nmsg\nWARNING:  msg\nDETAIL:  d\nHINT:  h\n"
        );
        let (r, out) = Captured::reporter(true, false, false);
        r.notice(&notice("DEBUG", None, None));
        r.notice(&notice("LOG", None, None));
        assert!(out.text().ends_with("] msg\n"), "{}", out.text());
        assert_eq!(out.text().lines().count(), 1);
    }

    fn n(sev: &str, i: usize) -> Notice {
        Notice {
            severity: Severity::from(sev),
            message: format!("m{i}"),
            detail: None,
            hint: None,
        }
    }

    #[test]
    fn notice_log_bounds() {
        let mut log = NoticeLog::default();
        for i in 0..1000 {
            log.push(&n(if i % 2 == 0 { "WARNING" } else { "NOTICE" }, i));
        }
        let (v, dropped) = log.finish();
        assert_eq!(v.as_array().map(Vec::len), Some(HEAD + WARNINGS + TAIL));
        assert_eq!(dropped, Some(1000 - (HEAD + WARNINGS + TAIL) as u64));
    }

    fn kept(log: &NoticeLog) -> Vec<String> {
        log.finish()
            .0
            .as_array()
            .unwrap()
            .iter()
            .map(|n| {
                format!(
                    "{} {}",
                    n["severity"].as_str().unwrap(),
                    n["message"].as_str().unwrap()
                )
            })
            .collect()
    }

    #[test]
    fn notice_log_keeps_the_head_the_middle_warnings_and_the_tail_in_order() {
        let mut log = NoticeLog::default();
        let total = HEAD + 10 + TAIL;
        for i in 0..total {
            let warning = i == HEAD + 3 || i == HEAD + 4;
            log.push(&n(if warning { "WARNING" } else { "NOTICE" }, i));
        }
        let mut want: Vec<String> = (0..HEAD).map(|i| format!("NOTICE m{i}")).collect();
        want.push(format!("WARNING m{}", HEAD + 3));
        want.push(format!("WARNING m{}", HEAD + 4));
        want.extend((HEAD + 10..total).map(|i| format!("NOTICE m{i}")));
        assert_eq!(kept(&log), want);
        assert_eq!(log.finish().1, Some(8));
    }

    #[test]
    fn notice_log_marks_no_truncation_within_its_bounds() {
        let mut log = NoticeLog::default();
        log.push(&n("NOTICE", 0));
        log.push(&n("LOG", 1));
        assert_eq!(kept(&log), ["NOTICE m0"]);
        assert_eq!(log.finish().1, None);
    }

    #[test]
    fn notice_log_caps_the_middle_warnings() {
        let mut log = NoticeLog::default();
        let total = 20_000;
        for i in 0..total {
            log.push(&n("WARNING", i));
        }
        let kept = kept(&log);
        assert_eq!(kept.len(), HEAD + WARNINGS + TAIL);
        assert_eq!(kept.first().map(String::as_str), Some("WARNING m0"));
        assert_eq!(kept.last(), Some(&format!("WARNING m{}", total - 1)));
        let (v, dropped) = log.finish();
        assert_eq!(dropped, Some((total - kept.len()) as u64));
        assert!(v.to_string().len() < 32 << 10);
    }

    #[test]
    fn notice_log_keeps_detail_and_hint() {
        let mut log = NoticeLog::default();
        log.push(&notice("WARNING", Some("d"), Some("y")));
        log.push(&notice("NOTICE", None, None));
        assert_eq!(
            log.finish().0,
            json!([
                {"severity": "WARNING", "message": "msg", "detail": "d", "hint": "y"},
                {"severity": "NOTICE", "message": "msg"}
            ])
        );
    }

    #[test]
    fn severities_keep_the_servers_word() {
        for word in [
            "DEBUG", "LOG", "INFO", "NOTICE", "WARNING", "ERROR", "AVISO",
        ] {
            assert_eq!(Severity::from(word).as_str(), word);
        }
        assert_eq!(Severity::from("WARNING"), Severity::Warning);
        assert_eq!(Severity::from("debug"), Severity::Other("debug".into()));
        let (r, out) = Captured::reporter(false, false, false);
        r.notice(&notice("AVISO", None, None));
        assert_eq!(out.text(), "AVISO:  msg\n");
        let mut log = NoticeLog::default();
        log.push(&notice("AVISO", None, None));
        log.push(&notice("LOG", None, None));
        log.push(&notice("WARNING", None, None));
        assert_eq!((log.total, log.warning_total), (2, 1));
        assert_eq!(log.finish().0[0]["severity"], "AVISO");
    }

    #[test]
    fn formats() {
        assert_eq!(seconds(std::time::Duration::from_millis(316)), "0.32s");
        let t = std::time::UNIX_EPOCH + std::time::Duration::from_millis(1_793_628_131_402);
        assert!(timestamp(t).ends_with(".402Z"));
    }
}
