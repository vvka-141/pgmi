//! The MCP line splitter and JSON-RPC decoder. Public for the `mcp_decoder`
//! fuzz target only; not a stable API.

use serde::de::{Deserialize, Deserializer, MapAccess, Visitor};
use serde_json::value::RawValue;

/// The longest line read, in bytes; a longer one is dropped unread.
pub const MAX_LINE: usize = 16 << 20;

/// Splits a byte stream into lines of at most `limit` bytes. A longer line
/// is never buffered: it is dropped and counted in `take_oversized`.
pub struct Lines {
    buf: Vec<u8>,
    limit: usize,
    skipping: bool,
    oversized: usize,
}

impl Default for Lines {
    fn default() -> Self {
        Lines::with_limit(MAX_LINE)
    }
}

impl Lines {
    #[must_use]
    pub fn with_limit(limit: usize) -> Self {
        Lines {
            buf: Vec::new(),
            limit,
            skipping: false,
            oversized: 0,
        }
    }

    pub fn feed(&mut self, chunk: &[u8]) -> Vec<Vec<u8>> {
        let mut out = Vec::new();
        let mut rest = chunk;
        while let Some(n) = rest.iter().position(|b| *b == b'\n') {
            self.append(&rest[..n]);
            if !std::mem::take(&mut self.skipping) {
                out.push(std::mem::take(&mut self.buf));
            }
            rest = &rest[n + 1..];
        }
        self.append(rest);
        out
    }

    pub fn finish(&mut self) -> Option<Vec<u8>> {
        let line = std::mem::take(&mut self.buf);
        (!std::mem::take(&mut self.skipping) && !line.is_empty()).then_some(line)
    }

    /// The number of lines dropped for their length since the last call.
    pub fn take_oversized(&mut self) -> usize {
        std::mem::take(&mut self.oversized)
    }

    fn append(&mut self, part: &[u8]) {
        if self.skipping {
            return;
        }
        if self.buf.len() + part.len() > self.limit {
            self.skipping = true;
            self.oversized += 1;
            self.buf = Vec::new();
        } else {
            self.buf.extend_from_slice(part);
        }
    }
}

#[derive(Debug)]
pub struct Request {
    pub id: Option<String>,
    pub method: String,
    pub params: Option<Box<RawValue>>,
}

#[derive(Debug)]
pub enum Decoded {
    Skip,
    Reply(String),
    Request(Request),
    Notification(Request),
}

pub(crate) const PARSE_ERROR: i64 = -32700;
pub(crate) const INVALID_REQUEST: i64 = -32600;
pub(crate) const METHOD_NOT_FOUND: i64 = -32601;
pub(crate) const INVALID_PARAMS: i64 = -32602;

pub fn decode(line: &[u8]) -> Decoded {
    if line.iter().all(|b| is_space(*b)) {
        return Decoded::Skip;
    }
    if let Err(e) = validate(line) {
        return Decoded::Reply(error(None, PARSE_ERROR, &format!("parse error: {e}")));
    }
    let text = lossy(line);
    let raw: &RawValue = match serde_json::from_str(&text) {
        Ok(r) => r,
        Err(e) => return Decoded::Reply(error(None, PARSE_ERROR, &format!("parse error: {e}"))),
    };
    if JsonKind::of(raw) == JsonKind::Null {
        return Decoded::Reply(error(
            None,
            INVALID_REQUEST,
            "invalid request: a request must be a JSON object, not null",
        ));
    }
    let fields = match go_struct(
        raw,
        "mcp.rpcRequest",
        "rpcRequest",
        &[
            ("jsonrpc", Field::String),
            ("id", Field::Raw),
            ("method", Field::String),
            ("params", Field::Raw),
        ],
    ) {
        Ok(f) => f,
        Err(e) => {
            return Decoded::Reply(error(
                None,
                INVALID_REQUEST,
                &format!("invalid request: {e}"),
            ));
        }
    };
    let id = fields.raw("id");
    if let Some(id) = id
        && !matches!(JsonKind::of(id), JsonKind::String | JsonKind::Number)
    {
        return Decoded::Reply(error(
            None,
            INVALID_REQUEST,
            "id must be a string or number",
        ));
    }
    let request = Request {
        id: id.map(|i| i.get().to_string()),
        method: fields.string("method").unwrap_or_default(),
        params: fields.raw("params").map(RawValue::to_owned),
    };
    let Some(id) = &request.id else {
        return Decoded::Notification(request);
    };
    if fields.string("jsonrpc").as_deref() != Some("2.0") {
        return Decoded::Reply(error(Some(id), INVALID_REQUEST, "jsonrpc must be \"2.0\""));
    }
    Decoded::Request(request)
}

pub(crate) fn error(id: Option<&str>, code: i64, message: &str) -> String {
    error_with(id, code, message, None)
}

pub(crate) fn error_with(
    id: Option<&str>,
    code: i64,
    message: &str,
    data: Option<&serde_json::Value>,
) -> String {
    let message = serde_json::to_string(message).unwrap_or_else(|_| "\"\"".into());
    let data = data
        .and_then(|d| serde_json::to_string(d).ok())
        .map(|d| format!(",\"data\":{d}"))
        .unwrap_or_default();
    format!(
        "{{\"jsonrpc\":\"2.0\",\"id\":{},\"error\":{{\"code\":{code},\"message\":{message}{data}}}}}",
        id.unwrap_or("null")
    )
}

pub(crate) fn result(id: &str, result: &str) -> String {
    format!("{{\"jsonrpc\":\"2.0\",\"id\":{id},\"result\":{result}}}")
}

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum Field {
    String,
    Raw,
}

pub(crate) struct Fields<'a> {
    values: Vec<(&'static str, &'a RawValue)>,
}

impl<'a> Fields<'a> {
    pub(crate) fn raw(&self, name: &str) -> Option<&'a RawValue> {
        self.values
            .iter()
            .rev()
            .find(|(n, _)| *n == name)
            .map(|(_, v)| *v)
    }

    pub(crate) fn string(&self, name: &str) -> Option<String> {
        self.raw(name)
            .and_then(|v| serde_json::from_str::<String>(v.get()).ok())
    }
}

/// The JSON type of a value, named as Go's `encoding/json` names it in its
/// errors.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum JsonKind {
    Object,
    Array,
    String,
    Bool,
    Null,
    Number,
}

impl JsonKind {
    pub(crate) fn of(raw: &RawValue) -> Self {
        match raw.get().bytes().find(|b| !is_space(*b)) {
            Some(b'{') => JsonKind::Object,
            Some(b'[') => JsonKind::Array,
            Some(b'"') => JsonKind::String,
            Some(b't' | b'f') => JsonKind::Bool,
            Some(b'n') => JsonKind::Null,
            _ => JsonKind::Number,
        }
    }

    pub(crate) fn of_value(v: &serde_json::Value) -> Self {
        match v {
            serde_json::Value::Null => JsonKind::Null,
            serde_json::Value::Bool(_) => JsonKind::Bool,
            serde_json::Value::Number(_) => JsonKind::Number,
            serde_json::Value::String(_) => JsonKind::String,
            serde_json::Value::Array(_) => JsonKind::Array,
            serde_json::Value::Object(_) => JsonKind::Object,
        }
    }
}

impl std::fmt::Display for JsonKind {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(match self {
            JsonKind::Object => "object",
            JsonKind::Array => "array",
            JsonKind::String => "string",
            JsonKind::Bool => "bool",
            JsonKind::Null => "null",
            JsonKind::Number => "number",
        })
    }
}

/// Why a value does not fit the Go struct it is read into, worded as Go's
/// `encoding/json`.
#[derive(Debug)]
pub(crate) enum StructError {
    NotAnObject {
        got: JsonKind,
        value_type: &'static str,
    },
    Syntax(serde_json::Error),
    Field {
        got: JsonKind,
        struct_name: &'static str,
        field: &'static str,
    },
}

impl std::fmt::Display for StructError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            StructError::NotAnObject { got, value_type } => {
                write!(
                    f,
                    "json: cannot unmarshal {got} into Go value of type {value_type}"
                )
            }
            StructError::Syntax(e) => write!(f, "json: {e}"),
            StructError::Field {
                got,
                struct_name,
                field,
            } => write!(
                f,
                "json: cannot unmarshal {got} into Go struct field {struct_name}.{field} of type string"
            ),
        }
    }
}

impl std::error::Error for StructError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            StructError::Syntax(e) => Some(e),
            _ => None,
        }
    }
}

pub(crate) fn go_struct<'a>(
    raw: &'a RawValue,
    value_type: &'static str,
    struct_name: &'static str,
    spec: &[(&'static str, Field)],
) -> Result<Fields<'a>, StructError> {
    let mut fields = Fields { values: Vec::new() };
    match JsonKind::of(raw) {
        JsonKind::Null => return Ok(fields),
        JsonKind::Object => {}
        got => return Err(StructError::NotAnObject { got, value_type }),
    }
    let members: Members<'a> = serde_json::from_str(raw.get()).map_err(StructError::Syntax)?;
    let mut first_error = None;
    for (key, value) in members.0 {
        let found = spec
            .iter()
            .find(|(n, _)| *n == key)
            .or_else(|| spec.iter().find(|(n, _)| n.eq_ignore_ascii_case(&key)));
        let Some((name, want)) = found else { continue };
        let got = JsonKind::of(value);
        if got == JsonKind::Null && *want != Field::Raw {
            continue;
        }
        let ok = match want {
            Field::Raw => true,
            Field::String => {
                got == JsonKind::String && serde_json::from_str::<String>(value.get()).is_ok()
            }
        };
        if !ok {
            if first_error.is_none() {
                first_error = Some(StructError::Field {
                    got,
                    struct_name,
                    field: name,
                });
            }
            continue;
        }
        fields.values.push((name, value));
    }
    match first_error {
        Some(e) => Err(e),
        None => Ok(fields),
    }
}

pub(crate) struct Members<'a>(pub Vec<(String, &'a RawValue)>);

impl<'de: 'a, 'a> Deserialize<'de> for Members<'a> {
    fn deserialize<D: Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
        struct V<'a>(std::marker::PhantomData<&'a ()>);
        impl<'de: 'a, 'a> Visitor<'de> for V<'a> {
            type Value = Members<'a>;
            fn expecting(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
                f.write_str("an object")
            }
            fn visit_map<M: MapAccess<'de>>(self, mut m: M) -> Result<Members<'a>, M::Error> {
                let mut out = Vec::new();
                while let Some((k, v)) = m.next_entry::<String, &'de RawValue>()? {
                    out.push((k, v));
                }
                Ok(Members(out))
            }
        }
        d.deserialize_map(V(std::marker::PhantomData))
    }
}

fn is_space(b: u8) -> bool {
    matches!(b, b' ' | b'\t' | b'\n' | b'\r')
}

pub(crate) fn lossy(bytes: &[u8]) -> String {
    let mut out = String::with_capacity(bytes.len());
    for chunk in bytes.utf8_chunks() {
        out.push_str(chunk.valid());
        for _ in chunk.invalid() {
            out.push('\u{FFFD}');
        }
    }
    out
}

fn quote_char(c: u8) -> String {
    match c {
        b'\'' => "'\\''".into(),
        b'"' => "'\"'".into(),
        b'\\' => "'\\\\'".into(),
        0x07 => "'\\a'".into(),
        0x08 => "'\\b'".into(),
        0x0c => "'\\f'".into(),
        b'\n' => "'\\n'".into(),
        b'\r' => "'\\r'".into(),
        b'\t' => "'\\t'".into(),
        0x0b => "'\\v'".into(),
        0x20..=0x7e => format!("'{}'", c as char),
        _ => format!("'\\x{c:02x}'"),
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Want {
    Value,
    ValueOrArrayEnd,
    KeyOrObjectEnd,
    Key,
    Colon,
    ObjectNext,
    ArrayNext,
    End,
}

/// Where an unexpected byte was met, in the words Go's `encoding/json` uses.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Place {
    AfterTopLevelValue,
    AfterObjectKey,
    AfterObjectPair,
    AfterArrayElement,
    ObjectKey,
    Value,
    HexEscape,
    StringEscape,
    StringLiteral,
    Number,
    AfterDecimalPoint,
    Exponent,
    Literal { word: &'static str, expecting: u8 },
}

/// A line that is not JSON, worded as Go's `encoding/json` words it.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum SyntaxError {
    UnexpectedEnd,
    InvalidCharacter { found: u8, place: Place },
}

impl std::fmt::Display for SyntaxError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let (found, place) = match self {
            SyntaxError::UnexpectedEnd => return f.write_str("unexpected end of JSON input"),
            SyntaxError::InvalidCharacter { found, place } => (*found, *place),
        };
        write!(f, "invalid character {} ", quote_char(found))?;
        match place {
            Place::AfterTopLevelValue => f.write_str("after top-level value"),
            Place::AfterObjectKey => f.write_str("after object key"),
            Place::AfterObjectPair => f.write_str("after object key:value pair"),
            Place::AfterArrayElement => f.write_str("after array element"),
            Place::ObjectKey => f.write_str("looking for beginning of object key string"),
            Place::Value => f.write_str("looking for beginning of value"),
            Place::HexEscape => f.write_str("in \\u hexadecimal character escape"),
            Place::StringEscape => f.write_str("in string escape code"),
            Place::StringLiteral => f.write_str("in string literal"),
            Place::Number => f.write_str("in numeric literal"),
            Place::AfterDecimalPoint => f.write_str("after decimal point in numeric literal"),
            Place::Exponent => f.write_str("in exponent of numeric literal"),
            Place::Literal { word, expecting } => {
                write!(f, "in literal {word} (expecting {})", quote_char(expecting))
            }
        }
    }
}

impl std::error::Error for SyntaxError {}

fn invalid(found: u8, place: Place) -> SyntaxError {
    SyntaxError::InvalidCharacter { found, place }
}

pub(crate) fn validate(b: &[u8]) -> Result<(), SyntaxError> {
    let bad = |c: u8, place: Place| Err(invalid(c, place));
    let mut stack: Vec<bool> = Vec::new();
    let mut want = Want::Value;
    let mut i = 0;
    loop {
        while i < b.len() && is_space(b[i]) {
            i += 1;
        }
        if i == b.len() {
            return if want == Want::End {
                Ok(())
            } else {
                Err(SyntaxError::UnexpectedEnd)
            };
        }
        let c = b[i];
        let mut closed = false;
        match want {
            Want::End => return bad(c, Place::AfterTopLevelValue),
            Want::Colon => {
                if c != b':' {
                    return bad(c, Place::AfterObjectKey);
                }
                i += 1;
                want = Want::Value;
                continue;
            }
            Want::ObjectNext => match c {
                b',' => {
                    i += 1;
                    want = Want::Key;
                    continue;
                }
                b'}' => closed = true,
                _ => return bad(c, Place::AfterObjectPair),
            },
            Want::ArrayNext => match c {
                b',' => {
                    i += 1;
                    want = Want::Value;
                    continue;
                }
                b']' => closed = true,
                _ => return bad(c, Place::AfterArrayElement),
            },
            Want::KeyOrObjectEnd | Want::Key => {
                if c == b'}' && want == Want::KeyOrObjectEnd {
                    closed = true;
                } else if c == b'"' {
                    i = string(b, i)?;
                    want = Want::Colon;
                    continue;
                } else {
                    return bad(c, Place::ObjectKey);
                }
            }
            Want::Value | Want::ValueOrArrayEnd => {
                if c == b']' && want == Want::ValueOrArrayEnd {
                    closed = true;
                } else {
                    match c {
                        b'{' => {
                            stack.push(true);
                            i += 1;
                            want = Want::KeyOrObjectEnd;
                            continue;
                        }
                        b'[' => {
                            stack.push(false);
                            i += 1;
                            want = Want::ValueOrArrayEnd;
                            continue;
                        }
                        b'"' => i = string(b, i)?,
                        b'-' | b'0'..=b'9' => i = number(b, i)?,
                        b't' => i = literal(b, i, "true")?,
                        b'f' => i = literal(b, i, "false")?,
                        b'n' => i = literal(b, i, "null")?,
                        _ => return bad(c, Place::Value),
                    }
                }
            }
        }
        if closed {
            stack.pop();
            i += 1;
        }
        want = match stack.last() {
            None => Want::End,
            Some(true) => Want::ObjectNext,
            Some(false) => Want::ArrayNext,
        };
    }
}

fn at(b: &[u8], i: usize) -> Result<u8, SyntaxError> {
    b.get(i).copied().ok_or(SyntaxError::UnexpectedEnd)
}

fn string(b: &[u8], start: usize) -> Result<usize, SyntaxError> {
    let mut i = start + 1;
    loop {
        let c = at(b, i)?;
        i += 1;
        match c {
            b'"' => return Ok(i),
            b'\\' => {
                let e = at(b, i)?;
                i += 1;
                match e {
                    b'"' | b'\\' | b'/' | b'b' | b'f' | b'n' | b'r' | b't' => {}
                    b'u' => {
                        for _ in 0..4 {
                            let h = at(b, i)?;
                            if !h.is_ascii_hexdigit() {
                                return Err(invalid(h, Place::HexEscape));
                            }
                            i += 1;
                        }
                    }
                    _ => {
                        return Err(invalid(e, Place::StringEscape));
                    }
                }
            }
            0..0x20 => {
                return Err(invalid(c, Place::StringLiteral));
            }
            _ => {}
        }
    }
}

fn digits(b: &[u8], mut i: usize) -> usize {
    while b.get(i).is_some_and(u8::is_ascii_digit) {
        i += 1;
    }
    i
}

fn number(b: &[u8], start: usize) -> Result<usize, SyntaxError> {
    let mut i = start;
    if b[i] == b'-' {
        i += 1;
        let c = at(b, i)?;
        if !c.is_ascii_digit() {
            return Err(invalid(c, Place::Number));
        }
    }
    i = if b[i] == b'0' { i + 1 } else { digits(b, i) };
    if b.get(i) == Some(&b'.') {
        i += 1;
        let c = at(b, i)?;
        if !c.is_ascii_digit() {
            return Err(invalid(c, Place::AfterDecimalPoint));
        }
        i = digits(b, i);
    }
    if matches!(b.get(i), Some(b'e' | b'E')) {
        i += 1;
        if matches!(at(b, i)?, b'+' | b'-') {
            i += 1;
        }
        let c = at(b, i)?;
        if !c.is_ascii_digit() {
            return Err(invalid(c, Place::Exponent));
        }
        i = digits(b, i);
    }
    Ok(i)
}

fn literal(b: &[u8], start: usize, word: &'static str) -> Result<usize, SyntaxError> {
    for (k, want) in word.bytes().enumerate().skip(1) {
        let c = at(b, start + k)?;
        if c != want {
            return Err(invalid(
                c,
                Place::Literal {
                    word,
                    expecting: want,
                },
            ));
        }
    }
    Ok(start + word.len())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn reply(line: &[u8]) -> String {
        match decode(line) {
            Decoded::Reply(r) => r,
            other => panic!("{other:?}"),
        }
    }

    #[test]
    fn go_parse_errors() {
        for (input, message) in [
            (
                &b"not json"[..],
                "invalid character 'o' in literal null (expecting 'u')",
            ),
            (b"{\"a\":1", "unexpected end of JSON input"),
            (
                b"\xff\xfe",
                "invalid character '\\xff' looking for beginning of value",
            ),
            (
                b"{\"a\":1} x",
                "invalid character 'x' after top-level value",
            ),
            (
                b"[1,]",
                "invalid character ']' looking for beginning of value",
            ),
            (
                b"{1:2}",
                "invalid character '1' looking for beginning of object key string",
            ),
            (b"{\"a\" 1}", "invalid character '1' after object key"),
            (
                b"{\"a\":1 2}",
                "invalid character '2' after object key:value pair",
            ),
            (b"[1 2]", "invalid character '2' after array element"),
            (b"-x", "invalid character 'x' in numeric literal"),
            (
                b"1.e",
                "invalid character 'e' after decimal point in numeric literal",
            ),
            (b"1e+", "unexpected end of JSON input"),
            (b"\"\\q\"", "invalid character 'q' in string escape code"),
            (
                b"\"\\u12g4\"",
                "invalid character 'g' in \\u hexadecimal character escape",
            ),
            (b"\"a\tb\"", "invalid character '\\t' in string literal"),
            (b"01", "invalid character '1' after top-level value"),
        ] {
            assert_eq!(
                validate(input).map_err(|e| e.to_string()),
                Err(message.to_string()),
                "{input:?}"
            );
        }
        for ok in [
            &b"0"[..],
            b"-0.5e+10",
            b" [ ] ",
            b"{\"a\":[1,{\"b\":null}]}",
            b"\"\xff\"",
        ] {
            assert_eq!(validate(ok), Ok(()), "{ok:?}");
        }
    }

    #[test]
    fn requests() {
        assert!(matches!(decode(b"   "), Decoded::Skip));
        let Decoded::Request(r) =
            decode(b"{\"jsonrpc\":\"2.0\",\"id\":\"a\xffb\",\"method\":\"ping\"}")
        else {
            panic!("not a request")
        };
        let id = r.id.unwrap_or_default();
        assert_eq!(
            serde_json::from_str::<String>(&id).ok().as_deref(),
            Some("a\u{fffd}b")
        );
        let Decoded::Reply(r) = decode(b"null") else {
            panic!("null was not answered")
        };
        assert_eq!(
            r,
            "{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32600,\"message\":\"invalid request: a request must be a JSON object, not null\"}}"
        );
        assert!(matches!(
            decode(b"{\"method\":\"x\"}"),
            Decoded::Notification(_)
        ));
        assert!(reply(b"[]").contains(
            "\"code\":-32600,\"message\":\"invalid request: json: cannot unmarshal array into Go value of type mcp.rpcRequest\""
        ));
        assert!(reply(b"{\"jsonrpc\":2,\"id\":4}").starts_with(
            "{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32600,\"message\":\"invalid request: json: cannot unmarshal number into Go struct field rpcRequest.jsonrpc of type string\""
        ));
        assert!(
            reply(b"{\"jsonrpc\":\"2.0\",\"id\":true}").contains("id must be a string or number")
        );
        assert!(
            reply(b"{\"id\":2,\"method\":\"ping\"}").starts_with("{\"jsonrpc\":\"2.0\",\"id\":2,")
        );
        let Decoded::Request(r) = decode(
            b"{\"JSONRPC\":\"2.0\",\"id\":1e2,\"Method\":\"ping\",\"params\":{\"x\":\"\xc3(\"}}",
        ) else {
            panic!()
        };
        assert_eq!((r.id.as_deref(), r.method.as_str()), (Some("1e2"), "ping"));
        assert_eq!(
            r.params.map(|p| p.get().to_string()).as_deref(),
            Some("{\"x\":\"\u{FFFD}(\"}")
        );
    }

    #[test]
    fn lines() {
        let mut l = Lines::default();
        assert_eq!(l.feed(b"a\nb"), vec![b"a".to_vec()]);
        assert_eq!(l.feed(b"c\n\nd"), vec![b"bc".to_vec(), vec![]]);
        assert_eq!(l.finish(), Some(b"d".to_vec()));
        assert_eq!(l.finish(), None);
    }

    #[test]
    fn a_line_over_the_limit_is_dropped_and_counted_once() {
        let mut l = Lines::with_limit(4);
        assert_eq!(l.feed(b"abcd\nab"), vec![b"abcd".to_vec()]);
        assert_eq!(l.take_oversized(), 0);
        assert!(l.feed(b"cde").is_empty());
        assert_eq!(l.take_oversized(), 1);
        assert!(l.buf.is_empty());
        assert_eq!(l.feed(b"fgh\nxy\n"), vec![b"xy".to_vec()]);
        assert_eq!(l.take_oversized(), 0);
        assert!(l.feed(b"123456").is_empty());
        assert_eq!(l.finish(), None);
        assert_eq!(l.take_oversized(), 1);
        assert_eq!(l.feed(b"ok\n"), vec![b"ok".to_vec()]);
    }

    proptest::proptest! {
        #[test]
        fn every_line_gets_at_most_one_answer(chunks in proptest::collection::vec(proptest::collection::vec(proptest::num::u8::ANY, 0..64), 0..8)) {
            let mut l = Lines::default();
            let mut all = Vec::new();
            for c in &chunks {
                all.extend(l.feed(c));
            }
            all.extend(l.finish());
            let joined: Vec<u8> = chunks.concat();
            let expected = joined.split(|b| *b == b'\n').filter(|s| !s.is_empty()).count();
            proptest::prop_assert!(all.iter().filter(|s| !s.is_empty()).count() == expected);
            for line in all {
                match decode(&line) {
                    Decoded::Reply(r) => {
                        proptest::prop_assert!(!r.contains('\n'));
                        proptest::prop_assert!(serde_json::from_str::<serde_json::Value>(&r).is_ok());
                    }
                    Decoded::Request(r) | Decoded::Notification(r) => {
                        if let Some(id) = r.id {
                            proptest::prop_assert!(serde_json::from_str::<serde_json::Value>(&id).is_ok());
                        }
                    }
                    Decoded::Skip => {}
                }
            }
        }

        #[test]
        fn lines_over_the_limit_are_dropped_and_the_rest_kept(chunks in proptest::collection::vec(proptest::collection::vec(proptest::sample::select(vec![b'a', b'\n']), 0..24), 0..8)) {
            let mut l = Lines::with_limit(8);
            let mut all = Vec::new();
            let mut oversized = 0;
            for c in &chunks {
                all.extend(l.feed(c));
                oversized += l.take_oversized();
            }
            all.extend(l.finish());
            let joined: Vec<u8> = chunks.concat();
            let segments: Vec<&[u8]> = joined.split(|b| *b == b'\n').collect();
            let kept: Vec<&[u8]> = segments.iter().copied().filter(|s| !s.is_empty() && s.len() <= 8).collect();
            proptest::prop_assert_eq!(all.iter().filter(|s| !s.is_empty()).map(Vec::as_slice).collect::<Vec<_>>(), kept);
            proptest::prop_assert_eq!(oversized, segments.iter().filter(|s| s.len() > 8).count());
        }

        #[test]
        fn validator_agrees_with_serde(s in "[\\[\\]{}\":,0-9a-z.eE+\\- \\\\]{0,24}") {
            let ours = validate(s.as_bytes()).is_ok();
            let theirs = serde_json::from_str::<serde::de::IgnoredAny>(&s).is_ok();
            proptest::prop_assert_eq!(ours, theirs, "{}", s);
        }
    }
}
