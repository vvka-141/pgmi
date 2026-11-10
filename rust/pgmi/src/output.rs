use std::collections::VecDeque;
use std::io::{IsTerminal, Write};
use std::sync::{Arc, Mutex};
use std::time::Instant;

use serde_json::{Map, Value, json};

use crate::conn::dial::Notice;
use crate::env::Env;

pub struct Reporter {
    verbose: bool,
    start: Instant,
    pub fancy: bool,
    pub color: bool,
    lock: Mutex<()>,
}

impl Reporter {
    pub fn new(verbose: bool, env: &Env) -> Arc<Self> {
        let term =
            std::io::stderr().is_terminal() && env.get("TERM").ok().flatten() != Some("dumb");
        let no_color = env.non_empty("NO_COLOR").ok().flatten().is_some();
        Arc::new(Reporter {
            verbose,
            start: Instant::now(),
            fancy: term,
            color: term && !no_color,
            lock: Mutex::new(()),
        })
    }

    pub fn line(&self, text: &str) {
        let _guard = self.lock.lock();
        let mut err = std::io::stderr().lock();
        if self.verbose {
            let t = self.start.elapsed().as_secs_f64();
            for l in text.lines() {
                let _ = writeln!(err, "[{t:.2}s] {l}");
            }
        } else {
            let _ = writeln!(err, "{text}");
        }
    }

    pub fn notice(&self, n: &Notice) {
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

    pub fn summary(&self, ok: bool, text: &str) {
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
pub struct NoticeLog {
    head: Vec<Value>,
    warnings: Vec<Value>,
    tail: VecDeque<Value>,
    dropped: u64,
    pub total: u64,
    pub warning_total: u64,
}

const HEAD: usize = 50;
const WARNINGS: usize = 200;
const TAIL: usize = 150;

impl NoticeLog {
    pub fn push(&mut self, n: &Notice) {
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

    pub fn finish(&self) -> (Value, Option<u64>) {
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

pub fn redact(text: &str, secrets: &[String]) -> String {
    let mut out = text.to_string();
    for s in secrets.iter().filter(|s| s.len() >= 3) {
        out = out.replace(s.as_str(), "***");
    }
    out
}

pub fn seconds(d: std::time::Duration) -> String {
    format!("{:.2}s", d.as_secs_f64())
}

pub fn timestamp(t: std::time::SystemTime) -> String {
    match jiff::Timestamp::try_from(t) {
        Ok(ts) => ts.strftime("%Y-%m-%dT%H:%M:%S%.3fZ").to_string(),
        Err(_) => String::new(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

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
        assert_eq!(
            redact("postgresql://u:secret@h", &["secret".into()]),
            "postgresql://u:***@h"
        );
        let t = std::time::UNIX_EPOCH + std::time::Duration::from_millis(1_793_628_131_402);
        assert!(timestamp(t).ends_with(".402Z"));
    }
}
