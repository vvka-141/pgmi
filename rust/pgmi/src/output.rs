use std::collections::VecDeque;
use std::io::{IsTerminal, Write};
use std::sync::{Arc, Mutex};
use std::time::Instant;

use serde_json::{Map, Value, json};

use crate::env::Env;

/// A server message: NOTICE, WARNING, DEBUG and the like.
#[derive(Debug, Clone)]
pub(crate) struct Notice {
    pub(crate) severity: String,
    pub(crate) message: String,
    pub(crate) detail: Option<String>,
    pub(crate) hint: Option<String>,
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
                out.push_str(&format!("[{t:.2}s] {l}\n"));
            }
        } else {
            out.push_str(text);
            out.push('\n');
        }
        let _ = sink.write_all(out.as_bytes());
        let _ = sink.flush();
    }

    pub(crate) fn notice(&self, n: &Notice) {
        match n.severity.as_str() {
            "LOG" => return,
            "DEBUG" if !self.verbose => return,
            _ => {}
        }
        let mut text = match n.severity.as_str() {
            "NOTICE" | "INFO" | "DEBUG" => n.message.clone(),
            s => format!("{s}:  {}", n.message),
        };
        if let Some(d) = &n.detail {
            text.push_str(&format!("\nDETAIL:  {d}"));
        }
        if let Some(h) = &n.hint {
            text.push_str(&format!("\nHINT:  {h}"));
        }
        self.line(&text);
    }

    pub(crate) fn summary(&self, ok: bool, text: &str) {
        let (word, open, close) = match (ok, self.fancy, self.color) {
            (true, true, true) => ("✓", "\x1b[32m", "\x1b[0m"),
            (true, true, false) => ("✓", "", ""),
            (true, false, _) => ("ok", "", ""),
            (false, true, true) => ("✗", "\x1b[31m", "\x1b[0m"),
            (false, true, false) => ("✗", "", ""),
            (false, false, _) => ("FAILED", "", ""),
        };
        self.line(&format!("{open}{word}{close} {text}"));
    }
}

#[derive(Default)]
pub(crate) struct NoticeLog {
    head: Vec<Value>,
    warnings: Vec<Value>,
    tail: VecDeque<Value>,
    dropped: u64,
    pub(crate) total: u64,
    pub(crate) warning_total: u64,
}

const HEAD: usize = 50;
const WARNINGS: usize = 200;
const TAIL: usize = 150;

impl NoticeLog {
    pub(crate) fn push(&mut self, n: &Notice) {
        if n.severity == "LOG" {
            return;
        }
        self.total += 1;
        if n.severity == "WARNING" {
            self.warning_total += 1;
        }
        let mut v = Map::new();
        v.insert("severity".into(), json!(n.severity));
        v.insert("message".into(), json!(n.message));
        if let Some(d) = &n.detail {
            v.insert("detail".into(), json!(d));
        }
        if let Some(h) = &n.hint {
            v.insert("hint".into(), json!(h));
        }
        let v = Value::Object(v);
        if self.head.len() < HEAD {
            self.head.push(v);
            return;
        }
        self.tail.push_back(v);
        if self.tail.len() > TAIL
            && let Some(old) = self.tail.pop_front()
        {
            if old["severity"] == "WARNING" && self.warnings.len() < WARNINGS {
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
            .cloned()
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
            severity: sev.into(),
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
            r.summary(ok, "app: done");
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
            severity: sev.into(),
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

    #[test]
    fn formats() {
        assert_eq!(seconds(std::time::Duration::from_millis(316)), "0.32s");
        let t = std::time::UNIX_EPOCH + std::time::Duration::from_millis(1_793_628_131_402);
        assert!(timestamp(t).ends_with(".402Z"));
    }
}
