use std::sync::mpsc;
use std::time::Duration;

use crossterm::event::{Event, KeyCode, KeyEvent, KeyEventKind, KeyModifiers};
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Paragraph, Wrap};

use super::select::{ACCENT, FAINT, MUTED, Outcome, Select};
use crate::config::yaml::Connection;
use crate::env::Env;
use crate::error::{Result, Secret};

const TITLE: &str = "pgmi - Connection Setup";
const MAINTENANCE: &str = "postgres";
const SPINNER: [&str; 8] = ["⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"];

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Method {
    Password,
    Azure,
    Aws,
    Google,
    Uri,
}

struct Provider {
    name: &'static str,
    detail: &'static str,
    methods: &'static [(Method, &'static str, &'static str)],
}

const PASSWORD: (Method, &str, &str) = (
    Method::Password,
    "Username and Password",
    "Standard PostgreSQL authentication",
);

const PROVIDERS: [Provider; 5] = [
    Provider {
        name: "Local / On-Premises",
        detail: "PostgreSQL on localhost or your own servers",
        methods: &[PASSWORD],
    },
    Provider {
        name: "Azure Database for PostgreSQL",
        detail: "Microsoft Azure managed PostgreSQL",
        methods: &[
            (
                Method::Azure,
                "Azure Entra ID (Recommended)",
                "Uses az login, managed identity, or environment variables",
            ),
            PASSWORD,
        ],
    },
    Provider {
        name: "AWS RDS PostgreSQL",
        detail: "Amazon Web Services managed PostgreSQL",
        methods: &[
            (
                Method::Aws,
                "IAM Database Authentication",
                "Uses AWS credentials for authentication",
            ),
            PASSWORD,
        ],
    },
    Provider {
        name: "Google Cloud SQL",
        detail: "Google Cloud managed PostgreSQL",
        methods: &[
            (
                Method::Google,
                "Cloud SQL IAM",
                "Uses Google Cloud credentials",
            ),
            PASSWORD,
        ],
    },
    Provider {
        name: "Other / Connection String",
        detail: "Enter a full PostgreSQL connection string",
        methods: &[(
            Method::Uri,
            "Connection String",
            "postgresql://user:pass@host:port/database",
        )],
    },
];

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub(crate) struct Settings {
    pub(crate) auth_method: Option<&'static str>,
    pub(crate) host: String,
    pub(crate) port: u16,
    pub(crate) database: String,
    pub(crate) maintenance: String,
    pub(crate) username: String,
    pub(crate) password: Secret,
    pub(crate) sslmode: String,
    pub(crate) sslcert: String,
    pub(crate) sslkey: String,
    pub(crate) sslrootcert: String,
    pub(crate) aws_region: String,
    pub(crate) google_instance: String,
}

impl Settings {
    pub(crate) fn yaml(&self) -> Vec<(&'static str, String)> {
        let port = if self.port == 0 {
            String::new()
        } else {
            self.port.to_string()
        };
        vec![
            ("host", self.host.clone()),
            ("port", port),
            ("username", self.username.clone()),
            ("database", self.database.clone()),
            ("maintenance_database", self.maintenance.clone()),
            ("sslmode", self.sslmode.clone()),
            ("sslcert", self.sslcert.clone()),
            ("sslkey", self.sslkey.clone()),
            ("sslrootcert", self.sslrootcert.clone()),
            (
                "auth_method",
                self.auth_method.unwrap_or_default().to_string(),
            ),
            ("aws_region", self.aws_region.clone()),
            ("google_instance", self.google_instance.clone()),
        ]
    }

    fn target(&self) -> String {
        if self.host.is_empty() && !self.google_instance.is_empty() {
            return format!("{}/{}", self.google_instance, self.database);
        }
        format!("{}:{}/{}", self.host, self.port, self.database)
    }

    fn token_label(&self) -> Option<&'static str> {
        match self.auth_method {
            Some("azure") => Some("Azure Entra ID"),
            Some("aws") => Some("AWS IAM"),
            Some("google") => Some("Google IAM"),
            _ => None,
        }
    }

    fn keyword_string(&self) -> String {
        let quote = |v: &str| format!("'{}'", v.replace('\\', "\\\\").replace('\'', "\\'"));
        let mut parts = vec![
            format!("host={}", quote(&self.host)),
            format!("port={}", self.port),
            format!("dbname={}", quote(&self.maintenance)),
            format!("user={}", quote(&self.username)),
        ];
        for (k, v) in [
            ("password", self.password.expose()),
            ("sslmode", self.sslmode.as_str()),
            ("sslcert", self.sslcert.as_str()),
            ("sslkey", self.sslkey.as_str()),
            ("sslrootcert", self.sslrootcert.as_str()),
        ] {
            if !v.is_empty() {
                parts.push(format!("{k}={}", quote(v)));
            }
        }
        parts.join(" ")
    }
}

#[derive(Debug, Clone)]
struct Input {
    label: &'static str,
    value: Vec<char>,
    cursor: usize,
    placeholder: &'static str,
    hint: Option<&'static str>,
    secret: bool,
    limit: usize,
}

impl Input {
    fn new(label: &'static str, value: &str, placeholder: &'static str, limit: usize) -> Self {
        let value: Vec<char> = value.chars().collect();
        Self {
            label,
            cursor: value.len(),
            value,
            placeholder,
            hint: None,
            secret: false,
            limit,
        }
    }

    fn hint(mut self, hint: &'static str) -> Self {
        self.hint = Some(hint);
        self
    }

    fn text(&self) -> String {
        self.value.iter().collect()
    }

    fn edit(&mut self, key: KeyEvent) {
        let control = key.modifiers.contains(KeyModifiers::CONTROL);
        match key.code {
            KeyCode::Char('u') if control => {
                self.value.drain(..self.cursor);
                self.cursor = 0;
            }
            KeyCode::Char('k') if control => self.value.truncate(self.cursor),
            KeyCode::Char('a') if control => self.cursor = 0,
            KeyCode::Char('e') if control => self.cursor = self.value.len(),
            KeyCode::Char(_) if control || key.modifiers.contains(KeyModifiers::ALT) => {}
            KeyCode::Char(c) if !c.is_control() && self.value.len() < self.limit => {
                self.value.insert(self.cursor, c);
                self.cursor += 1;
            }
            KeyCode::Backspace if self.cursor > 0 => {
                self.cursor -= 1;
                self.value.remove(self.cursor);
            }
            KeyCode::Delete if self.cursor < self.value.len() => {
                self.value.remove(self.cursor);
            }
            KeyCode::Left => self.cursor = self.cursor.saturating_sub(1),
            KeyCode::Right => self.cursor = (self.cursor + 1).min(self.value.len()),
            KeyCode::Home => self.cursor = 0,
            KeyCode::End => self.cursor = self.value.len(),
            _ => {}
        }
    }

    fn spans(&self, focused: bool) -> Vec<Span<'static>> {
        if self.value.is_empty() {
            let mut out = Vec::new();
            if focused {
                out.push(Span::styled(
                    " ",
                    Style::new().add_modifier(Modifier::REVERSED),
                ));
            }
            out.push(Span::styled(self.placeholder, Style::new().fg(FAINT)));
            return out;
        }
        let shown: Vec<char> = if self.secret {
            vec!['•'; self.value.len()]
        } else {
            self.value.clone()
        };
        if !focused {
            return vec![Span::raw(shown.iter().collect::<String>())];
        }
        let before: String = shown[..self.cursor].iter().collect();
        let at: String = shown.get(self.cursor).map_or(" ".into(), |c| c.to_string());
        let after: String = shown
            .get(self.cursor + 1..)
            .map_or(String::new(), |r| r.iter().collect());
        vec![
            Span::raw(before),
            Span::styled(at, Style::new().add_modifier(Modifier::REVERSED)),
            Span::raw(after),
        ]
    }
}

#[derive(Debug, Clone)]
struct Form {
    method: Method,
    subtitle: &'static str,
    inputs: Vec<Input>,
    focus: usize,
    error: String,
    notes: &'static [&'static str],
}

enum FormOutcome {
    Submit,
    Back,
}

impl Form {
    fn new(provider: usize, method: Method, initial: Option<&Connection>) -> Self {
        let (subtitle, inputs, notes): (_, Vec<Input>, &'static [&'static str]) = match method {
            Method::Password => {
                let pick = |f: fn(&Connection) -> &Option<String>, default: &str| {
                    initial
                        .and_then(|c| f(c).clone())
                        .filter(|v| !v.is_empty())
                        .unwrap_or_else(|| default.to_string())
                };
                let host_default = if provider == 0 { "localhost" } else { "" };
                let mut password = Input::new("Password:", "", "Enter password", 256)
                    .hint("leave empty to use ~/.pgpass or $PGPASSWORD");
                password.secret = true;
                (
                    "Connection Details",
                    vec![
                        Input::new("Host:", &pick(|c| &c.host, host_default), "hostname", 256),
                        Input::new(
                            "Port:",
                            &initial
                                .and_then(|c| c.port.as_deref())
                                .and_then(|p| p.parse::<u16>().ok())
                                .filter(|p| *p > 0)
                                .unwrap_or(5432)
                                .to_string(),
                            "",
                            5,
                        ),
                        Input::new("Database (required):", &pick(|c| &c.database, ""), "", 64)
                            .hint("target database — created automatically if it doesn't exist"),
                        Input::new(
                            "Maintenance DB:",
                            &pick(|c| &c.maintenance_database, MAINTENANCE),
                            "",
                            64,
                        )
                        .hint("existing database pgmi connects to for server-level operations"),
                        Input::new("Username:", &pick(|c| &c.username, "postgres"), "", 64),
                        password,
                    ],
                    &[],
                )
            }
            Method::Azure => (
                "Azure PostgreSQL - Entra ID",
                vec![
                    Input::new("Server:", "", "myserver.postgres.database.azure.com", 256),
                    Input::new("Database:", "", "mydb", 64),
                    Input::new("Username:", "", "user@myserver", 128),
                ],
                &["Authentication uses Azure CLI (az login) or environment variables."],
            ),
            Method::Aws => (
                "AWS RDS - IAM Authentication",
                vec![
                    Input::new("Host:", "", "mydb.xxx.us-east-1.rds.amazonaws.com", 256),
                    Input::new("Port:", "5432", "", 5),
                    Input::new("Database:", "", "mydb", 64),
                    Input::new("Username:", "", "iam_user", 64),
                    Input::new("Region:", "", "us-east-1", 32),
                ],
                &["Authentication uses AWS credentials (env vars, config file, or IAM role)."],
            ),
            Method::Google => (
                "Google Cloud SQL - IAM",
                vec![
                    Input::new("Instance:", "", "project:region:instance", 256),
                    Input::new("Database:", "", "mydb", 64),
                    Input::new("Username:", "", "iam_user@project.iam", 128),
                ],
                &[
                    "Instance format: project:region:instance",
                    "Authentication uses gcloud or service account.",
                ],
            ),
            Method::Uri => (
                "Connection String",
                vec![Input::new(
                    "PostgreSQL URI:",
                    "",
                    "postgresql://user:password@host:5432/database",
                    512,
                )],
                &["Format: postgresql://user:password@host:port/database"],
            ),
        };
        Self {
            method,
            subtitle,
            inputs,
            focus: 0,
            error: String::new(),
            notes,
        }
    }

    fn update(&mut self, key: KeyEvent) -> Option<FormOutcome> {
        let last = self.inputs.len() - 1;
        match key.code {
            KeyCode::Tab | KeyCode::Down => self.focus = (self.focus + 1).min(last),
            KeyCode::BackTab | KeyCode::Up => self.focus = self.focus.saturating_sub(1),
            KeyCode::Enter if self.focus < last => self.focus += 1,
            KeyCode::Enter => return Some(FormOutcome::Submit),
            KeyCode::Esc => return Some(FormOutcome::Back),
            _ => {
                self.error.clear();
                self.inputs[self.focus].edit(key);
            }
        }
        None
    }

    fn value(&self, i: usize) -> String {
        self.inputs[i].text()
    }

    fn port(&self, i: usize) -> u16 {
        self.value(i)
            .parse::<u16>()
            .ok()
            .filter(|p| *p > 0)
            .unwrap_or(5432)
    }

    fn settings(&self) -> std::result::Result<Settings, (usize, String)> {
        let required = |i: usize, what: &str| {
            if self.value(i).is_empty() {
                Err((i, format!("{what} is required")))
            } else {
                Ok(())
            }
        };
        let or = |v: String, d: &str| if v.is_empty() { d.to_string() } else { v };
        Ok(match self.method {
            Method::Password => {
                required(2, "database name")?;
                Settings {
                    host: or(self.value(0), "localhost"),
                    port: self.port(1),
                    database: self.value(2),
                    maintenance: or(self.value(3), MAINTENANCE),
                    username: or(self.value(4), "postgres"),
                    password: self.value(5).into(),
                    sslmode: "prefer".into(),
                    ..Settings::default()
                }
            }
            Method::Azure => {
                required(0, "server name")?;
                required(1, "database name")?;
                Settings {
                    auth_method: Some("azure"),
                    host: self.value(0),
                    port: 5432,
                    database: self.value(1),
                    maintenance: MAINTENANCE.into(),
                    username: self.value(2),
                    sslmode: "require".into(),
                    ..Settings::default()
                }
            }
            Method::Aws => {
                required(0, "host")?;
                required(2, "database name")?;
                Settings {
                    auth_method: Some("aws"),
                    host: self.value(0),
                    port: self.port(1),
                    database: self.value(2),
                    maintenance: MAINTENANCE.into(),
                    username: self.value(3),
                    aws_region: self.value(4),
                    sslmode: "require".into(),
                    ..Settings::default()
                }
            }
            Method::Google => {
                required(0, "instance connection name")?;
                required(1, "database name")?;
                Settings {
                    auth_method: Some("google"),
                    google_instance: self.value(0),
                    database: self.value(1),
                    maintenance: MAINTENANCE.into(),
                    username: self.value(2),
                    ..Settings::default()
                }
            }
            Method::Uri => {
                required(0, "connection string")?;
                let f = crate::conn::parse::parse(&self.value(0)).map_err(|e| (0, e.text()))?;
                let get = |k: &str| f.get(k).cloned().unwrap_or_default();
                let port = match f.get("port") {
                    None => 5432,
                    Some(p) => p
                        .parse::<u16>()
                        .map_err(|_| (0, "invalid connection string: give one port".to_string()))?,
                };
                Settings {
                    host: or(get("host"), "localhost"),
                    port,
                    database: get("dbname"),
                    maintenance: MAINTENANCE.into(),
                    username: get("user"),
                    password: get("password").into(),
                    sslmode: get("sslmode"),
                    sslcert: get("sslcert"),
                    sslkey: get("sslkey"),
                    sslrootcert: get("sslrootcert"),
                    ..Settings::default()
                }
            }
        })
    }

    fn lines(&self) -> Vec<Line<'static>> {
        let mut lines = vec![subtitle(self.subtitle)];
        let width = self
            .inputs
            .iter()
            .map(|i| i.label.chars().count())
            .max()
            .unwrap_or(0);
        for (i, input) in self.inputs.iter().enumerate() {
            let focused = i == self.focus;
            let label = format!("{:<width$}", input.label);
            let mut spans = if focused {
                vec![Span::styled(
                    format!("› {label}"),
                    Style::new().fg(ACCENT).add_modifier(Modifier::BOLD),
                )]
            } else {
                vec![Span::styled(format!("  {label}"), Style::new().fg(MUTED))]
            };
            spans.push(Span::raw(" "));
            spans.extend(input.spans(focused));
            lines.push(Line::from(spans));
            if !focused {
                continue;
            }
            if let Some(h) = input.hint {
                lines.push(detail(h));
            }
            if !self.error.is_empty() {
                lines.push(Line::from(Span::styled(
                    format!("  Error: {}", self.error),
                    Style::new().fg(Color::Indexed(196)),
                )));
            }
        }
        lines.push(Line::default());
        for n in self.notes {
            lines.push(detail(n));
        }
        if !self.notes.is_empty() {
            lines.push(Line::default());
        }
        lines.push(help(if self.method == Method::Uri {
            "enter submit • esc back"
        } else {
            "tab/↓ next • shift+tab/↑ prev • enter submit • esc back"
        }));
        lines
    }
}

fn subtitle(text: &str) -> Line<'static> {
    Line::from(Span::styled(text.to_string(), Style::new().fg(MUTED)))
}

fn detail(text: &str) -> Line<'static> {
    Line::from(Span::styled(format!("    {text}"), Style::new().fg(FAINT)))
}

fn help(text: &'static str) -> Line<'static> {
    Line::from(Span::styled(text, Style::new().fg(FAINT)))
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum Test {
    Running(usize),
    Passed(String),
    Failed(String),
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Done {
    Finished(Box<Settings>),
    Cancelled,
    Interrupted,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Effect {
    Test(Box<Settings>),
    Done(Done),
}

pub(crate) struct Wizard {
    provider: Select,
    auth: Option<Select>,
    form: Option<Form>,
    test: Option<(Settings, Test)>,
    initial: Option<Connection>,
}

impl Wizard {
    pub(crate) fn new(initial: Option<Connection>) -> Self {
        Self {
            provider: Select {
                prompt: "Where is your PostgreSQL server?".into(),
                choices: PROVIDERS
                    .iter()
                    .map(|p| (p.name.to_string(), p.detail.to_string()))
                    .collect(),
                help: "↑/↓ navigate • enter select • esc back • ctrl+c quit",
                cursor: 0,
            },
            auth: None,
            form: None,
            test: None,
            initial,
        }
    }

    pub(crate) fn page(&self) -> (u8, u8) {
        let test = match &self.test {
            Some((_, Test::Running(_))) => 1,
            Some((_, Test::Passed(_))) => 2,
            Some((_, Test::Failed(_))) => 3,
            None => 0,
        };
        let stage = if self.test.is_some() {
            3
        } else if self.form.is_some() {
            2
        } else {
            u8::from(self.auth.is_some())
        };
        (stage, test)
    }

    fn methods(&self) -> &'static [(Method, &'static str, &'static str)] {
        PROVIDERS[self.provider.cursor].methods
    }

    fn open_form(&mut self, method: Method) {
        let initial = self.initial.as_ref();
        self.form = Some(Form::new(self.provider.cursor, method, initial));
    }

    pub(crate) fn update(&mut self, key: KeyEvent) -> Option<Effect> {
        if key.kind == KeyEventKind::Release {
            return None;
        }
        if key.code == KeyCode::Char('c') && key.modifiers.contains(KeyModifiers::CONTROL) {
            return Some(Effect::Done(Done::Interrupted));
        }
        if let Some((settings, test)) = &mut self.test {
            return match (test, key.code) {
                (Test::Running(_), _) => None,
                (Test::Passed(_), KeyCode::Enter) => {
                    Some(Effect::Done(Done::Finished(Box::new(settings.clone()))))
                }
                (Test::Failed(_), KeyCode::Enter) => {
                    let s = settings.clone();
                    self.test = Some((s.clone(), Test::Running(0)));
                    Some(Effect::Test(Box::new(s)))
                }
                (_, KeyCode::Esc) => {
                    self.test = None;
                    if let Some(f) = &mut self.form {
                        f.focus = if f.method == Method::Password {
                            f.inputs.len() - 1
                        } else {
                            0
                        };
                    }
                    None
                }
                _ => None,
            };
        }
        if let Some(form) = &mut self.form {
            return match form.update(key)? {
                FormOutcome::Back => {
                    self.form = None;
                    None
                }
                FormOutcome::Submit => match form.settings() {
                    Err((field, message)) => {
                        form.error = message;
                        form.focus = field;
                        None
                    }
                    Ok(mut s) => {
                        form.error.clear();
                        if let Some(i) =
                            self.initial.as_ref().filter(|_| form.method != Method::Uri)
                        {
                            keep_tls(&mut s, i);
                        }
                        self.test = Some((s.clone(), Test::Running(0)));
                        Some(Effect::Test(Box::new(s)))
                    }
                },
            };
        }
        if let Some(auth) = &mut self.auth {
            return match auth.update(key)? {
                Outcome::Picked(i) => {
                    let method = self.methods()[i].0;
                    self.open_form(method);
                    None
                }
                Outcome::Back => {
                    self.auth = None;
                    None
                }
                Outcome::Cancel => Some(Effect::Done(Done::Interrupted)),
            };
        }
        match self.provider.update(key)? {
            Outcome::Picked(_) => {
                let methods = self.methods();
                if methods.len() == 1 {
                    self.open_form(methods[0].0);
                } else {
                    self.auth = Some(Select {
                        prompt: format!(
                            "{} - Authentication",
                            PROVIDERS[self.provider.cursor].name
                        ),
                        choices: methods
                            .iter()
                            .map(|(_, n, d)| (n.to_string(), d.to_string()))
                            .collect(),
                        help: "↑/↓ navigate • enter select • esc back",
                        cursor: 0,
                    });
                }
                None
            }
            Outcome::Back => Some(Effect::Done(Done::Cancelled)),
            Outcome::Cancel => Some(Effect::Done(Done::Interrupted)),
        }
    }

    pub(crate) fn tested(&mut self, result: std::result::Result<String, String>) {
        if let Some((_, t)) = &mut self.test {
            *t = match result {
                Ok(info) => Test::Passed(info),
                Err(e) => Test::Failed(e),
            };
        }
    }

    pub(crate) fn tick(&mut self) {
        if let Some((_, Test::Running(n))) = &mut self.test {
            *n = (*n + 1) % SPINNER.len();
        }
    }

    pub(crate) fn view(&self) -> Paragraph<'static> {
        let lines = if let Some((s, t)) = &self.test {
            test_lines(s, t)
        } else if let Some(f) = &self.form {
            f.lines()
        } else if let Some(a) = &self.auth {
            a.view(TITLE).split_off(2)
        } else {
            self.provider.view(TITLE).split_off(2)
        };
        let mut all = vec![
            Line::from(Span::styled(
                TITLE,
                Style::new().fg(ACCENT).add_modifier(Modifier::BOLD),
            )),
            Line::default(),
        ];
        all.extend(lines);
        Paragraph::new(all).wrap(Wrap { trim: false })
    }
}

fn keep_tls(s: &mut Settings, initial: &Connection) {
    let token = matches!(s.auth_method, Some("azure" | "aws"));
    if let Some(mode) = initial.sslmode.as_deref().filter(|m| !m.is_empty())
        && (!token || matches!(mode, "require" | "verify-ca" | "verify-full"))
    {
        s.sslmode = mode.to_string();
    }
    for (slot, kept) in [
        (&mut s.sslcert, &initial.sslcert),
        (&mut s.sslkey, &initial.sslkey),
        (&mut s.sslrootcert, &initial.sslrootcert),
    ] {
        if slot.is_empty()
            && let Some(k) = kept
        {
            slot.clone_from(k);
        }
    }
}

fn test_lines(s: &Settings, t: &Test) -> Vec<Line<'static>> {
    let mut lines = vec![
        subtitle("Testing Connection"),
        Line::default(),
        Line::raw(format!("Target: {}", s.target())),
        Line::default(),
    ];
    let success = Style::new().fg(Color::Indexed(34));
    match t {
        Test::Running(n) => lines.push(Line::from(vec![
            Span::styled(SPINNER[*n], Style::new().fg(ACCENT)),
            Span::raw(" Connecting..."),
        ])),
        Test::Passed(info) => {
            if s.token_label().is_none() {
                lines.push(Line::styled(
                    format!("✓ Connected to {}:{}/{}", s.host, s.port, s.maintenance),
                    success,
                ));
                if !s.database.is_empty() && s.database != s.maintenance {
                    lines.push(detail(&format!(
                        "{} is created on the first deploy if it does not exist",
                        s.database
                    )));
                }
            } else {
                lines.push(Line::styled("✓ Connected", success));
            }
            lines.push(detail(info));
            lines.push(Line::default());
            lines.push(help("enter continue • esc go back"));
        }
        Test::Failed(e) => {
            lines.push(Line::styled(
                "✗ Connection failed",
                Style::new().fg(Color::Indexed(196)),
            ));
            for l in e.lines() {
                lines.push(detail(l));
            }
            lines.push(Line::default());
            lines.push(help("enter try again • esc go back"));
        }
    }
    lines
}

pub(crate) async fn test(s: Settings, env: Env) -> std::result::Result<String, String> {
    if let Some(label) = s.token_label() {
        return Ok(format!(
            "{label} authentication configured (not tested — token is fetched at deploy time)"
        ));
    }
    let flags = crate::conn::resolve::Flags {
        connection: Some(s.keyword_string().into()),
        ..Default::default()
    };
    let spec = crate::conn::resolve::resolve(&flags, &env, None).map_err(|e| e.text())?;
    let dialer = crate::conn::dial::Dialer {
        notices: std::sync::Arc::new(|_| {}),
        log: std::sync::Arc::new(|_| {}),
    };
    let work = async {
        let session = dialer
            .connect(&spec, &s.maintenance)
            .await
            .map_err(|e| e.text())?;
        let row = session
            .client
            .query_one("SELECT version()", &[])
            .await
            .map_err(|e| e.to_string())?;
        let version: String = row.try_get(0).map_err(|e| e.to_string())?;
        Ok(version
            .split_once(',')
            .map_or(version.clone(), |(v, _)| v.to_string()))
    };
    tokio::time::timeout(Duration::from_secs(10), work)
        .await
        .unwrap_or_else(|_| Err("timeout: no answer within 10s".into()))
}

pub(crate) fn run(initial: Option<Connection>, env: &Env) -> Result<Done> {
    let mut wizard = Wizard::new(initial);
    let mut screen = super::Screen::open()?;
    let (tx, rx) = mpsc::channel();
    let mut page = wizard.page();
    loop {
        screen
            .0
            .draw(|f| f.render_widget(wizard.view(), f.area()))
            .map_err(super::io)?;
        if let Ok(result) = rx.try_recv() {
            wizard.tested(result);
        } else if crossterm::event::poll(Duration::from_millis(100)).map_err(super::io)? {
            if let Event::Key(key) = crossterm::event::read().map_err(super::io)? {
                match wizard.update(key) {
                    Some(Effect::Done(done)) => return Ok(done),
                    Some(Effect::Test(s)) => {
                        let tx = tx.clone();
                        let env = env.clone();
                        std::thread::spawn(move || {
                            let result = tokio::runtime::Builder::new_current_thread()
                                .enable_all()
                                .build()
                                .map_err(|e| e.to_string())
                                .and_then(|rt| rt.block_on(test(*s, env)));
                            let _ = tx.send(result);
                        });
                    }
                    None => {}
                }
            }
        } else {
            wizard.tick();
        }
        if wizard.page() != page {
            page = wizard.page();
            screen.reset()?;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn key(code: KeyCode) -> KeyEvent {
        KeyEvent::new(code, KeyModifiers::NONE)
    }

    fn ctrl(c: char) -> KeyEvent {
        KeyEvent::new(KeyCode::Char(c), KeyModifiers::CONTROL)
    }

    fn typed(w: &mut Wizard, text: &str) {
        for c in text.chars() {
            assert_eq!(w.update(key(KeyCode::Char(c))), None);
        }
    }

    #[test]
    fn local_form_as_the_case_types_it() {
        let mut w = Wizard::new(None);
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert_eq!(w.update(ctrl('u')), None);
        typed(&mut w, "127.0.0.1");
        w.update(key(KeyCode::Tab));
        w.update(ctrl('u'));
        typed(&mut w, "6543");
        w.update(key(KeyCode::Tab));
        typed(&mut w, "app");
        w.update(key(KeyCode::Tab));
        w.update(key(KeyCode::Tab));
        w.update(key(KeyCode::Tab));
        typed(&mut w, "pw");
        let want = Settings {
            host: "127.0.0.1".into(),
            port: 6543,
            database: "app".into(),
            maintenance: "postgres".into(),
            username: "postgres".into(),
            password: "pw".into(),
            sslmode: "prefer".into(),
            ..Settings::default()
        };
        assert_eq!(
            w.update(key(KeyCode::Enter)),
            Some(Effect::Test(Box::new(want.clone())))
        );
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        w.tested(Ok("PostgreSQL 18".into()));
        assert_eq!(
            w.update(key(KeyCode::Enter)),
            Some(Effect::Done(Done::Finished(Box::new(want))))
        );
    }

    #[test]
    fn database_is_required() {
        let mut w = Wizard::new(None);
        w.update(key(KeyCode::Enter));
        for _ in 0..5 {
            w.update(key(KeyCode::Tab));
        }
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        let f = w.form.as_ref().unwrap();
        assert_eq!(f.focus, 2);
        assert_eq!(f.error, "database name is required");
    }

    #[test]
    fn esc_walks_back_and_ctrl_c_interrupts() {
        let mut w = Wizard::new(None);
        w.update(key(KeyCode::Down));
        w.update(key(KeyCode::Enter));
        assert!(w.auth.is_some());
        w.update(key(KeyCode::Enter));
        assert_eq!(w.form.as_ref().unwrap().method, Method::Azure);
        w.update(key(KeyCode::Esc));
        assert!(w.form.is_none() && w.auth.is_some());
        w.update(key(KeyCode::Esc));
        assert!(w.auth.is_none());
        assert_eq!(
            w.update(key(KeyCode::Esc)),
            Some(Effect::Done(Done::Cancelled))
        );
        assert_eq!(
            Wizard::new(None).update(ctrl('c')),
            Some(Effect::Done(Done::Interrupted))
        );
    }

    #[test]
    fn google_iam_settings() {
        let mut w = Wizard::new(None);
        for _ in 0..3 {
            w.update(key(KeyCode::Down));
        }
        w.update(key(KeyCode::Enter));
        w.update(key(KeyCode::Enter));
        typed(&mut w, "p:r:i");
        w.update(key(KeyCode::Enter));
        typed(&mut w, "db");
        w.update(key(KeyCode::Enter));
        typed(&mut w, "me@p.iam");
        let Some(Effect::Test(s)) = w.update(key(KeyCode::Enter)) else {
            panic!("no test");
        };
        assert_eq!(s.auth_method, Some("google"));
        assert_eq!(s.target(), "p:r:i/db");
        let yaml = s.yaml();
        assert!(yaml.contains(&("port", String::new())));
        assert!(yaml.contains(&("google_instance", "p:r:i".into())));
    }

    #[test]
    fn connection_string_is_parsed() {
        let mut w = Wizard::new(None);
        w.update(key(KeyCode::Up));
        w.update(key(KeyCode::Enter));
        typed(
            &mut w,
            "postgresql://u:s%3Acret@db.example:6000/app?sslmode=require",
        );
        let Some(Effect::Test(s)) = w.update(key(KeyCode::Enter)) else {
            panic!("no test");
        };
        assert_eq!(
            (
                s.host.as_str(),
                s.port,
                s.database.as_str(),
                s.username.as_str()
            ),
            ("db.example", 6000, "app", "u")
        );
        assert_eq!(s.password.expose(), "s:cret");
        assert_eq!(s.sslmode, "require");
    }

    #[test]
    fn existing_settings_seed_the_form_and_keep_tls() {
        let initial = Connection {
            host: Some("db".into()),
            port: Some("6000".into()),
            database: Some("app".into()),
            sslmode: Some("verify-full".into()),
            sslrootcert: Some("/ca.pem".into()),
            ..Connection::default()
        };
        let mut w = Wizard::new(Some(initial));
        w.update(key(KeyCode::Enter));
        for _ in 0..5 {
            assert_eq!(w.update(key(KeyCode::Enter)), None);
        }
        let Some(Effect::Test(s)) = w.update(key(KeyCode::Enter)) else {
            panic!("no test");
        };
        assert_eq!((s.host.as_str(), s.port), ("db", 6000));
        assert_eq!(s.sslmode, "verify-full");
        assert_eq!(s.sslrootcert, "/ca.pem");
    }

    #[test]
    fn input_editing() {
        let mut i = Input::new("x", "abc", "", 4);
        i.edit(key(KeyCode::Left));
        i.edit(key(KeyCode::Char('X')));
        i.edit(key(KeyCode::Char('Y')));
        assert_eq!(i.text(), "abXc");
        i.edit(key(KeyCode::Backspace));
        assert_eq!(i.text(), "abc");
        i.edit(ctrl('u'));
        assert_eq!(i.text(), "c");
        i.edit(ctrl('j'));
        assert_eq!(i.text(), "c");
    }
}
