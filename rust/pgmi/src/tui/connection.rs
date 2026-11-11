use std::time::Duration;

use crossterm::event::{KeyCode, KeyEvent, KeyEventKind, KeyModifiers};
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Paragraph, Wrap};

use super::select::{ACCENT, Choice, FAINT, MUTED, Outcome, Select};
use crate::config::yaml::Connection;
use crate::conn::cloud::Provider as Cloud;
use crate::conn::resolve::SslMode;
use crate::env::Env;
use crate::error::{Error, Result, Secret};

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

#[derive(Debug, PartialEq, Eq)]
struct Provider {
    name: &'static str,
    detail: &'static str,
    host: &'static str,
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
        host: "localhost",
        methods: &[PASSWORD],
    },
    Provider {
        name: "Azure Database for PostgreSQL",
        detail: "Microsoft Azure managed PostgreSQL",
        host: "",
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
        host: "",
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
        host: "",
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
        host: "",
        methods: &[(
            Method::Uri,
            "Connection String",
            "postgresql://user:pass@host:port/database",
        )],
    },
];

/// What the wizard collected; `None` is a setting the user left out, and
/// pgmi.yaml then gets no key for it.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub(crate) struct Settings {
    pub(crate) auth: Option<Cloud>,
    pub(crate) host: Option<String>,
    pub(crate) port: Option<u16>,
    pub(crate) database: Option<String>,
    pub(crate) maintenance: String,
    pub(crate) username: Option<String>,
    pub(crate) password: Option<Secret>,
    pub(crate) sslmode: Option<String>,
    pub(crate) sslcert: Option<String>,
    pub(crate) sslkey: Option<String>,
    pub(crate) sslrootcert: Option<String>,
    pub(crate) aws_region: Option<String>,
    pub(crate) google_instance: Option<String>,
}

impl Settings {
    pub(crate) fn yaml(&self) -> Vec<(&'static str, Option<String>)> {
        vec![
            ("host", self.host.clone()),
            ("port", self.port.map(|p| p.to_string())),
            ("username", self.username.clone()),
            ("database", self.database.clone()),
            ("maintenance_database", Some(self.maintenance.clone())),
            ("sslmode", self.sslmode.clone()),
            ("sslcert", self.sslcert.clone()),
            ("sslkey", self.sslkey.clone()),
            ("sslrootcert", self.sslrootcert.clone()),
            ("auth_method", self.auth.map(|a| a.key().to_string())),
            ("aws_region", self.aws_region.clone()),
            ("google_instance", self.google_instance.clone()),
        ]
    }

    fn host(&self) -> &str {
        self.host.as_deref().unwrap_or_default()
    }

    fn database(&self) -> &str {
        self.database.as_deref().unwrap_or_default()
    }

    fn target(&self) -> String {
        match (&self.host, &self.google_instance) {
            (None, Some(instance)) => format!("{instance}/{}", self.database()),
            _ => format!(
                "{}:{}/{}",
                self.host(),
                self.port.unwrap_or_default(),
                self.database()
            ),
        }
    }

    fn token_label(&self) -> Option<&'static str> {
        match self.auth? {
            Cloud::Azure => Some("Azure Entra ID"),
            Cloud::Aws => Some("AWS IAM"),
            Cloud::Google => Some("Google IAM"),
            Cloud::Databricks => None,
        }
    }

    fn keyword_string(&self) -> String {
        let quote = |v: &str| format!("'{}'", v.replace('\\', "\\\\").replace('\'', "\\'"));
        let mut parts = vec![
            format!("host={}", quote(self.host())),
            format!("port={}", self.port.unwrap_or_default()),
            format!("dbname={}", quote(&self.maintenance)),
            format!(
                "user={}",
                quote(self.username.as_deref().unwrap_or_default())
            ),
        ];
        for (k, v) in [
            ("password", self.password.as_ref().map(Secret::expose)),
            ("sslmode", self.sslmode.as_deref()),
            ("sslcert", self.sslcert.as_deref()),
            ("sslkey", self.sslkey.as_deref()),
            ("sslrootcert", self.sslrootcert.as_deref()),
        ] {
            if let Some(v) = v {
                parts.push(format!("{k}={}", quote(v)));
            }
        }
        parts.join(" ")
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Field {
    Host,
    Port,
    Database,
    Maintenance,
    Username,
    Password,
    Region,
    Instance,
    Uri,
}

#[derive(Debug, Clone)]
struct Input {
    field: Field,
    label: &'static str,
    value: Vec<char>,
    cursor: usize,
    placeholder: &'static str,
    hint: Option<&'static str>,
    secret: bool,
    limit: usize,
}

impl Input {
    fn new(
        field: Field,
        label: &'static str,
        value: &str,
        placeholder: &'static str,
        limit: usize,
    ) -> Self {
        let value: Vec<char> = value.chars().collect();
        Self {
            field,
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
        let at: String = shown
            .get(self.cursor)
            .map_or(" ".into(), std::string::ToString::to_string);
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
    fn new(provider: &Provider, method: Method, initial: Option<&Connection>) -> Self {
        let (subtitle, inputs, notes): (_, Vec<Input>, &'static [&'static str]) = match method {
            Method::Password => {
                let pick = |f: fn(&Connection) -> &Option<String>, default: &str| {
                    initial
                        .and_then(|c| f(c).clone())
                        .filter(|v| !v.is_empty())
                        .unwrap_or_else(|| default.to_string())
                };
                let host_default = provider.host;
                let mut password =
                    Input::new(Field::Password, "Password:", "", "Enter password", 256)
                        .hint("leave empty to use ~/.pgpass or $PGPASSWORD");
                password.secret = true;
                (
                    "Connection Details",
                    vec![
                        Input::new(
                            Field::Host,
                            "Host:",
                            &pick(|c| &c.host, host_default),
                            "hostname",
                            256,
                        ),
                        Input::new(
                            Field::Port,
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
                        Input::new(
                            Field::Database,
                            "Database (required):",
                            &pick(|c| &c.database, ""),
                            "",
                            64,
                        )
                        .hint("target database — created automatically if it doesn't exist"),
                        Input::new(
                            Field::Maintenance,
                            "Maintenance DB:",
                            &pick(|c| &c.maintenance_database, MAINTENANCE),
                            "",
                            64,
                        )
                        .hint("existing database pgmi connects to for server-level operations"),
                        Input::new(
                            Field::Username,
                            "Username:",
                            &pick(|c| &c.username, "postgres"),
                            "",
                            64,
                        ),
                        password,
                    ],
                    &[],
                )
            }
            Method::Azure => (
                "Azure PostgreSQL - Entra ID",
                vec![
                    Input::new(
                        Field::Host,
                        "Server:",
                        "",
                        "myserver.postgres.database.azure.com",
                        256,
                    ),
                    Input::new(Field::Database, "Database:", "", "mydb", 64),
                    Input::new(Field::Username, "Username:", "", "user@myserver", 128),
                ],
                &["Authentication uses Azure CLI (az login) or environment variables."],
            ),
            Method::Aws => (
                "AWS RDS - IAM Authentication",
                vec![
                    Input::new(
                        Field::Host,
                        "Host:",
                        "",
                        "mydb.xxx.us-east-1.rds.amazonaws.com",
                        256,
                    ),
                    Input::new(Field::Port, "Port:", "5432", "", 5),
                    Input::new(Field::Database, "Database:", "", "mydb", 64),
                    Input::new(Field::Username, "Username:", "", "iam_user", 64),
                    Input::new(Field::Region, "Region:", "", "us-east-1", 32),
                ],
                &["Authentication uses AWS credentials (env vars, config file, or IAM role)."],
            ),
            Method::Google => (
                "Google Cloud SQL - IAM",
                vec![
                    Input::new(
                        Field::Instance,
                        "Instance:",
                        "",
                        "project:region:instance",
                        256,
                    ),
                    Input::new(Field::Database, "Database:", "", "mydb", 64),
                    Input::new(
                        Field::Username,
                        "Username:",
                        "",
                        "iam_user@project.iam",
                        128,
                    ),
                ],
                &[
                    "Instance format: project:region:instance",
                    "Authentication uses gcloud or service account.",
                ],
            ),
            Method::Uri => (
                "Connection String",
                vec![Input::new(
                    Field::Uri,
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

    fn index(&self, field: Field) -> usize {
        self.inputs
            .iter()
            .position(|i| i.field == field)
            .unwrap_or_default()
    }

    fn filled(&self, field: Field) -> Option<String> {
        self.inputs
            .iter()
            .find(|i| i.field == field)
            .map(Input::text)
            .filter(|v| !v.is_empty())
    }

    fn port(&self) -> u16 {
        self.filled(Field::Port)
            .and_then(|p| p.parse::<u16>().ok())
            .filter(|p| *p > 0)
            .unwrap_or(5432)
    }

    fn required(&self, field: Field, what: &str) -> std::result::Result<String, (Field, String)> {
        self.filled(field)
            .ok_or_else(|| (field, format!("{what} is required")))
    }

    fn settings(&self) -> std::result::Result<Settings, (Field, String)> {
        let or = |f: Field, d: &str| self.filled(f).unwrap_or_else(|| d.to_string());
        Ok(match self.method {
            Method::Password => Settings {
                database: Some(self.required(Field::Database, "database name")?),
                host: Some(or(Field::Host, "localhost")),
                port: Some(self.port()),
                maintenance: or(Field::Maintenance, MAINTENANCE),
                username: Some(or(Field::Username, "postgres")),
                password: self.filled(Field::Password).map(Secret::from),
                sslmode: Some(SslMode::Prefer.to_string()),
                ..Settings::default()
            },
            Method::Azure => Settings {
                auth: Some(Cloud::Azure),
                host: Some(self.required(Field::Host, "server name")?),
                database: Some(self.required(Field::Database, "database name")?),
                port: Some(5432),
                maintenance: MAINTENANCE.into(),
                username: self.filled(Field::Username),
                sslmode: Some(SslMode::Require.to_string()),
                ..Settings::default()
            },
            Method::Aws => Settings {
                auth: Some(Cloud::Aws),
                host: Some(self.required(Field::Host, "host")?),
                database: Some(self.required(Field::Database, "database name")?),
                port: Some(self.port()),
                maintenance: MAINTENANCE.into(),
                username: self.filled(Field::Username),
                aws_region: self.filled(Field::Region),
                sslmode: Some(SslMode::Require.to_string()),
                ..Settings::default()
            },
            Method::Google => Settings {
                auth: Some(Cloud::Google),
                google_instance: Some(self.required(Field::Instance, "instance connection name")?),
                database: Some(self.required(Field::Database, "database name")?),
                maintenance: MAINTENANCE.into(),
                username: self.filled(Field::Username),
                ..Settings::default()
            },
            Method::Uri => {
                let uri = self.required(Field::Uri, "connection string")?;
                let mut f =
                    crate::conn::parse::parse(&uri).map_err(|e| (Field::Uri, e.to_string()))?;
                let port = f.remove("port");
                let mut take = |k: &str| f.remove(k).filter(|v| !v.is_empty());
                let port = match port {
                    None => 5432,
                    Some(p) => p.parse::<u16>().map_err(|_| {
                        (
                            Field::Uri,
                            "invalid connection string: give one port".to_string(),
                        )
                    })?,
                };
                Settings {
                    host: Some(take("host").unwrap_or_else(|| "localhost".into())),
                    port: Some(port),
                    database: take("dbname"),
                    maintenance: MAINTENANCE.into(),
                    username: take("user"),
                    password: take("password").map(Secret::from),
                    sslmode: take("sslmode"),
                    sslcert: take("sslcert"),
                    sslkey: take("sslkey"),
                    sslrootcert: take("sslrootcert"),
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
pub(crate) enum Effect<D = Done> {
    Test(Box<Settings>),
    Done(D),
}

pub(crate) struct Wizard {
    provider: Select<&'static Provider>,
    auth: Option<(&'static Provider, Select<Method>)>,
    form: Option<Form>,
    test: Option<(Settings, Test)>,
    initial: Option<Connection>,
}

impl Wizard {
    pub(crate) fn new(initial: Option<Connection>) -> Self {
        Self {
            provider: Select::new(
                "Where is your PostgreSQL server?",
                PROVIDERS
                    .iter()
                    .map(|p| Choice::new(p, p.name, p.detail))
                    .collect(),
                "↑/↓ navigate • enter select • esc back • ctrl+c quit",
            ),
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

    fn open_form(&mut self, provider: &Provider, method: Method) {
        let initial = self.initial.as_ref();
        self.form = Some(Form::new(provider, method, initial));
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
                        form.focus = form.index(field);
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
        if let Some((provider, auth)) = &mut self.auth {
            return match auth.update(key)? {
                Outcome::Picked(method) => {
                    let provider = *provider;
                    self.open_form(provider, method);
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
            Outcome::Picked(provider) => {
                match provider.methods {
                    [(only, _, _)] => self.open_form(provider, *only),
                    methods => {
                        self.auth = Some((
                            provider,
                            Select::new(
                                format!("{} - Authentication", provider.name),
                                methods
                                    .iter()
                                    .map(|(m, n, d)| Choice::new(*m, *n, *d))
                                    .collect(),
                                "↑/↓ navigate • enter select • esc back",
                            ),
                        ));
                    }
                }
                None
            }
            Outcome::Back => Some(Effect::Done(Done::Cancelled)),
            Outcome::Cancel => Some(Effect::Done(Done::Interrupted)),
        }
    }

    pub(crate) fn tested(&mut self, result: TestResult) {
        if let Some((_, t)) = &mut self.test {
            *t = match result {
                Ok(info) => Test::Passed(info),
                Err(e) => Test::Failed(e.to_string()),
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
        } else if let Some((_, a)) = &self.auth {
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
    let token = matches!(s.auth, Some(Cloud::Azure | Cloud::Aws));
    if let Some(mode) = initial.sslmode.as_deref().filter(|m| !m.is_empty())
        && (!token || mode.parse::<SslMode>().is_ok_and(|m| !m.allows_plaintext()))
    {
        s.sslmode = Some(mode.to_string());
    }
    for (slot, kept) in [
        (&mut s.sslcert, &initial.sslcert),
        (&mut s.sslkey, &initial.sslkey),
        (&mut s.sslrootcert, &initial.sslrootcert),
    ] {
        if slot.is_none() {
            *slot = kept.clone().filter(|k| !k.is_empty());
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
                    format!(
                        "✓ Connected to {}:{}/{}",
                        s.host(),
                        s.port.unwrap_or_default(),
                        s.maintenance
                    ),
                    success,
                ));
                if let Some(db) = s.database.as_ref().filter(|d| **d != s.maintenance) {
                    lines.push(detail(&format!(
                        "{db} is created on the first deploy if it does not exist"
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

/// Why the wizard's connection test failed.
#[derive(Debug)]
pub(crate) enum TestError {
    Settings(crate::conn::resolve::ResolveError),
    Connect(Error),
    Query(tokio_postgres::Error),
    Timeout,
}

impl std::fmt::Display for TestError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            TestError::Settings(e) => e.fmt(f),
            TestError::Connect(e) => e.fmt(f),
            TestError::Query(e) => e.fmt(f),
            TestError::Timeout => f.write_str("timeout: no answer within 10s"),
        }
    }
}

impl std::error::Error for TestError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            TestError::Settings(e) => Some(e),
            TestError::Connect(e) => Some(e),
            TestError::Query(e) => Some(e),
            TestError::Timeout => None,
        }
    }
}

/// The server version line, or why the test failed.
pub(crate) type TestResult = std::result::Result<String, TestError>;

pub(crate) async fn test(s: Settings, env: Env) -> TestResult {
    if let Some(label) = s.token_label() {
        return Ok(format!(
            "{label} authentication configured (not tested — token is fetched at deploy time)"
        ));
    }
    let flags = crate::conn::resolve::Flags {
        connection: Some(s.keyword_string().into()),
        ..Default::default()
    };
    let spec = crate::conn::resolve::resolve(&flags, &env, None).map_err(TestError::Settings)?;
    let dialer = crate::conn::dial::Dialer {
        notices: std::sync::Arc::new(|_| {}),
        log: std::sync::Arc::new(|_| {}),
        secret: std::sync::Arc::new(|_| {}),
    };
    let work = async {
        let session = dialer
            .connect(&spec, &s.maintenance)
            .await
            .map_err(TestError::Connect)?;
        let row = session
            .client
            .query_one("SELECT version()", &[])
            .await
            .map_err(TestError::Query)?;
        let version: String = row.try_get(0).map_err(TestError::Query)?;
        Ok(version
            .split_once(',')
            .map_or(version.clone(), |(v, _)| v.to_string()))
    };
    tokio::time::timeout(Duration::from_secs(10), work)
        .await
        .unwrap_or(Err(TestError::Timeout))
}

impl super::Steps for Wizard {
    type Done = Done;

    fn update(&mut self, key: KeyEvent) -> Option<Effect> {
        Wizard::update(self, key)
    }

    fn tested(&mut self, result: TestResult) {
        Wizard::tested(self, result);
    }

    fn tick(&mut self) {
        Wizard::tick(self);
    }

    fn screen(&self) -> (u8, u8, u8) {
        let (stage, test) = self.page();
        (0, stage, test)
    }

    fn view(&self) -> Paragraph<'static> {
        Wizard::view(self)
    }
}

pub(crate) fn run(
    initial: Option<Connection>,
    env: &Env,
    runtime: &tokio::runtime::Handle,
) -> Result<Done> {
    super::drive(&mut Wizard::new(initial), env, runtime)
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
            host: Some("127.0.0.1".into()),
            port: Some(6543),
            database: Some("app".into()),
            maintenance: "postgres".into(),
            username: Some("postgres".into()),
            password: Some("pw".into()),
            sslmode: Some("prefer".into()),
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
        assert_eq!(s.auth, Some(Cloud::Google));
        assert_eq!(s.target(), "p:r:i/db");
        let yaml = s.yaml();
        assert!(yaml.contains(&("port", None)));
        assert!(yaml.contains(&("host", None)));
        assert!(yaml.contains(&("auth_method", Some("google".into()))));
        assert!(yaml.contains(&("google_instance", Some("p:r:i".into()))));
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
                s.host.as_deref(),
                s.port,
                s.database.as_deref(),
                s.username.as_deref()
            ),
            (Some("db.example"), Some(6000), Some("app"), Some("u"))
        );
        assert_eq!(s.password.as_ref().map(Secret::expose), Some("s:cret"));
        assert_eq!(s.sslmode.as_deref(), Some("require"));
        assert_eq!(s.sslrootcert, None);
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
        assert_eq!((s.host.as_deref(), s.port), (Some("db"), Some(6000)));
        assert_eq!(s.sslmode.as_deref(), Some("verify-full"));
        assert_eq!(s.sslrootcert.as_deref(), Some("/ca.pem"));
    }

    #[test]
    fn input_editing() {
        let mut i = Input::new(Field::Host, "x", "abc", "", 4);
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

    fn text(lines: &[Line<'_>]) -> String {
        lines
            .iter()
            .map(|l| {
                l.spans
                    .iter()
                    .map(|s| s.content.as_ref())
                    .collect::<String>()
            })
            .collect::<Vec<_>>()
            .join("\n")
    }

    fn screen(w: &Wizard, width: u16, height: u16) -> String {
        use ratatui::buffer::Buffer;
        use ratatui::layout::Rect;
        use ratatui::widgets::Widget;
        let area = Rect::new(0, 0, width, height);
        let mut buf = Buffer::empty(area);
        w.view().render(area, &mut buf);
        (0..height)
            .map(|y| (0..width).map(|x| buf[(x, y)].symbol()).collect::<String>())
            .collect::<Vec<_>>()
            .join("\n")
    }

    fn values(w: &Wizard) -> Vec<String> {
        w.form
            .as_ref()
            .unwrap()
            .inputs
            .iter()
            .map(Input::text)
            .collect()
    }

    fn focus(w: &Wizard) -> usize {
        w.form.as_ref().unwrap().focus
    }

    fn local_with_database(w: &mut Wizard, db: &str) -> Option<Effect> {
        w.update(key(KeyCode::Enter));
        w.update(key(KeyCode::Enter));
        w.update(key(KeyCode::Enter));
        typed(w, db);
        for _ in 0..3 {
            w.update(key(KeyCode::Enter));
        }
        w.update(key(KeyCode::Enter))
    }

    fn provider(w: &mut Wizard, downs: usize) {
        for _ in 0..downs {
            w.update(key(KeyCode::Down));
        }
        w.update(key(KeyCode::Enter));
    }

    fn defaults(db: &str) -> Settings {
        Settings {
            host: Some("localhost".into()),
            port: Some(5432),
            database: Some(db.into()),
            maintenance: "postgres".into(),
            username: Some("postgres".into()),
            sslmode: Some("prefer".into()),
            ..Settings::default()
        }
    }

    #[test]
    fn the_wizard_starts_on_the_first_provider() {
        let w = Wizard::new(None);
        assert_eq!(w.provider.cursor, 0);
        assert_eq!(w.page(), (0, 0));
        let mut w = Wizard::new(None);
        w.update(key(KeyCode::Down));
        assert_eq!(w.provider.cursor, 1);
        w.update(key(KeyCode::Up));
        assert_eq!(w.provider.cursor, 0);
    }

    #[test]
    fn the_provider_list_wraps_at_both_ends() {
        let mut w = Wizard::new(None);
        w.update(key(KeyCode::Up));
        assert_eq!(w.provider.cursor, PROVIDERS.len() - 1);
        w.update(key(KeyCode::Down));
        assert_eq!(w.provider.cursor, 0);
    }

    #[test]
    fn local_opens_the_host_form_with_its_defaults() {
        let mut w = Wizard::new(None);
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert!(w.auth.is_none());
        assert_eq!(w.form.as_ref().unwrap().method, Method::Password);
        assert_eq!(
            values(&w),
            ["localhost", "5432", "", "postgres", "postgres", ""]
        );
        assert_eq!(w.page(), (2, 0));
    }

    #[test]
    fn enter_walks_the_fields_then_submits_the_defaults() {
        let mut w = Wizard::new(None);
        w.update(key(KeyCode::Enter));
        for want in 1..=2 {
            assert_eq!(w.update(key(KeyCode::Enter)), None);
            assert_eq!(focus(&w), want);
        }
        typed(&mut w, "testdb");
        for want in 3..=5 {
            assert_eq!(w.update(key(KeyCode::Enter)), None);
            assert_eq!(focus(&w), want);
        }
        assert_eq!(
            w.update(key(KeyCode::Enter)),
            Some(Effect::Test(Box::new(defaults("testdb"))))
        );
        assert_eq!(w.page(), (3, 1));
        w.tested(Ok("PostgreSQL 16.1".into()));
        assert_eq!(w.page(), (3, 2));
        assert_eq!(
            w.update(key(KeyCode::Enter)),
            Some(Effect::Done(Done::Finished(Box::new(defaults("testdb")))))
        );
    }

    #[test]
    fn tab_and_backtab_move_focus_within_the_form() {
        let mut w = Wizard::new(None);
        w.update(key(KeyCode::Enter));
        w.update(key(KeyCode::BackTab));
        assert_eq!(focus(&w), 0);
        w.update(key(KeyCode::Tab));
        assert_eq!(focus(&w), 1);
        w.update(key(KeyCode::BackTab));
        assert_eq!(focus(&w), 0);
        for _ in 0..6 {
            w.update(key(KeyCode::Tab));
        }
        assert_eq!(focus(&w), 5);
    }

    #[test]
    fn a_missing_database_is_named_under_its_field_and_typing_clears_it() {
        let mut w = Wizard::new(None);
        assert_eq!(local_with_database(&mut w, ""), None);
        let f = w.form.as_ref().unwrap();
        assert_eq!(
            (f.focus, f.error.as_str()),
            (2, "database name is required")
        );
        let view = text(&f.lines());
        let (err, maint) = (
            view.find("database name is required").unwrap(),
            view.find("Maintenance DB:").unwrap(),
        );
        assert!(err < maint, "{view}");
        w.update(key(KeyCode::Char('x')));
        assert_eq!(w.form.as_ref().unwrap().error, "");
    }

    #[test]
    fn esc_after_a_failed_test_returns_to_the_form_with_the_values_kept() {
        let mut w = Wizard::new(None);
        w.update(key(KeyCode::Enter));
        w.update(key(KeyCode::Enter));
        w.update(key(KeyCode::Enter));
        typed(&mut w, "trialb");
        for _ in 0..3 {
            w.update(key(KeyCode::Enter));
        }
        typed(&mut w, "nope");
        let before = values(&w);
        assert!(matches!(
            w.update(key(KeyCode::Enter)),
            Some(Effect::Test(_))
        ));
        w.tested(Err(TestError::Connect(Error::connection("refused"))));
        assert_eq!(w.page(), (3, 3));
        assert!(
            text(&test_lines(
                &defaults("trialb"),
                &Test::Failed("refused".into())
            ))
            .contains("✗ Connection failed")
        );
        assert_eq!(w.update(key(KeyCode::Esc)), None);
        assert_eq!(w.page(), (2, 0));
        assert_eq!(values(&w), before);
        assert_eq!(focus(&w), 5);
    }

    #[test]
    fn enter_after_a_failed_test_tests_the_same_settings_again() {
        let mut w = Wizard::new(None);
        let Some(Effect::Test(first)) = local_with_database(&mut w, "testdb") else {
            panic!("no test");
        };
        assert_eq!(first.maintenance, "postgres");
        w.tested(Err(TestError::Timeout));
        assert_eq!(
            w.update(key(KeyCode::Enter)),
            Some(Effect::Test(first.clone()))
        );
        assert_eq!(w.page(), (3, 1));
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        w.tested(Ok("PostgreSQL 16.1".into()));
        assert_eq!(
            w.update(key(KeyCode::Enter)),
            Some(Effect::Done(Done::Finished(first)))
        );
    }

    #[test]
    fn the_success_screen_names_the_maintenance_database() {
        let s = defaults("testdb");
        let view = text(&test_lines(&s, &Test::Passed("PostgreSQL 18.0".into())));
        assert!(
            view.contains("✓ Connected to localhost:5432/postgres"),
            "{view}"
        );
        assert!(
            view.contains("testdb is created on the first deploy"),
            "{view}"
        );
    }

    #[test]
    fn azure_entra_id_takes_three_fields_and_needs_a_database() {
        let mut w = Wizard::new(None);
        provider(&mut w, 1);
        assert!(w.auth.is_some() && w.form.is_none());
        w.update(key(KeyCode::Enter));
        assert_eq!(w.form.as_ref().unwrap().inputs.len(), 3);
        typed(&mut w, "myserver.postgres.database.azure.com");
        w.update(key(KeyCode::Enter));
        w.update(key(KeyCode::Enter));
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert_eq!(w.form.as_ref().unwrap().error, "database name is required");
        assert_eq!(focus(&w), 1);
        typed(&mut w, "testdb");
        w.update(key(KeyCode::Enter));
        let Some(Effect::Test(s)) = w.update(key(KeyCode::Enter)) else {
            panic!("no test");
        };
        assert_eq!(
            (s.auth, s.sslmode.as_deref(), s.port),
            (Some(Cloud::Azure), Some("require"), Some(5432))
        );
        w.tested(Ok("configured".into()));
        assert!(matches!(
            w.update(key(KeyCode::Enter)),
            Some(Effect::Done(Done::Finished(_)))
        ));
    }

    #[test]
    fn aws_iam_takes_five_fields_and_needs_a_host() {
        let mut w = Wizard::new(None);
        provider(&mut w, 2);
        w.update(key(KeyCode::Enter));
        assert_eq!(w.form.as_ref().unwrap().inputs.len(), 5);
        for _ in 0..4 {
            w.update(key(KeyCode::Enter));
        }
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert_eq!(w.form.as_ref().unwrap().error, "host is required");
        assert_eq!(focus(&w), 0);
        typed(&mut w, "mydb.xxx.us-east-1.rds.amazonaws.com");
        w.update(key(KeyCode::Enter));
        w.update(key(KeyCode::Enter));
        typed(&mut w, "mydb");
        w.update(key(KeyCode::Enter));
        typed(&mut w, "iam_user");
        w.update(key(KeyCode::Enter));
        typed(&mut w, "us-east-1");
        let Some(Effect::Test(s)) = w.update(key(KeyCode::Enter)) else {
            panic!("no test");
        };
        assert_eq!(
            (
                s.auth,
                s.aws_region.as_deref(),
                s.port,
                s.database.as_deref()
            ),
            (
                Some(Cloud::Aws),
                Some("us-east-1"),
                Some(5432),
                Some("mydb")
            )
        );
    }

    #[test]
    fn google_iam_needs_an_instance() {
        let mut w = Wizard::new(None);
        provider(&mut w, 3);
        w.update(key(KeyCode::Enter));
        assert_eq!(w.form.as_ref().unwrap().inputs.len(), 3);
        w.update(key(KeyCode::Enter));
        typed(&mut w, "mydb");
        w.update(key(KeyCode::Enter));
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert_eq!(
            w.form.as_ref().unwrap().error,
            "instance connection name is required"
        );
    }

    #[test]
    fn a_connection_string_is_required_and_must_parse() {
        let mut w = Wizard::new(None);
        provider(&mut w, 4);
        assert_eq!(w.form.as_ref().unwrap().inputs.len(), 1);
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert_eq!(
            w.form.as_ref().unwrap().error,
            "connection string is required"
        );
        typed(&mut w, "postgresql://user:pass@localhost:5432/mydb");
        let Some(Effect::Test(s)) = w.update(key(KeyCode::Enter)) else {
            panic!("no test");
        };
        assert_eq!(s.maintenance, "postgres");
        w.tested(Ok("Connected".into()));
        assert!(matches!(
            w.update(key(KeyCode::Enter)),
            Some(Effect::Done(Done::Finished(_)))
        ));
        let mut w = Wizard::new(None);
        provider(&mut w, 4);
        typed(&mut w, "not-a-valid-connection-string");
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert!(!w.form.as_ref().unwrap().error.is_empty());
        assert!(w.test.is_none());
    }

    #[test]
    fn an_unparseable_connection_string_never_shows_its_password() {
        let mut w = Wizard::new(None);
        provider(&mut w, 4);
        typed(
            &mut w,
            "postgresql://user:pa%ssword-SECRET@localhost:5432/db",
        );
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        let error = &w.form.as_ref().unwrap().error;
        assert_eq!(error, "invalid percent-encoding in connection string");
    }

    #[test]
    fn an_unusable_port_falls_back_to_5432() {
        let mut w = Wizard::new(None);
        w.update(key(KeyCode::Enter));
        w.update(key(KeyCode::Tab));
        w.update(ctrl('u'));
        typed(&mut w, "abc");
        assert_eq!(values(&w)[1], "abc");
        let Some(Effect::Test(s)) = ({
            w.update(key(KeyCode::Tab));
            typed(&mut w, "testdb");
            for _ in 0..3 {
                w.update(key(KeyCode::Tab));
            }
            w.update(key(KeyCode::Enter))
        }) else {
            panic!("no test");
        };
        assert_eq!(s.port, Some(5432));
    }

    #[test]
    fn q_is_not_a_key_and_the_footer_does_not_offer_it() {
        let mut w = Wizard::new(None);
        assert_eq!(w.update(key(KeyCode::Char('q'))), None);
        assert_eq!(w.page(), (0, 0));
        assert!(
            !w.provider
                .view(TITLE)
                .iter()
                .any(|l| text(std::slice::from_ref(l)).contains("q quit"))
        );
    }

    #[test]
    fn every_screen_shows_its_title_and_choices() {
        let w = Wizard::new(None);
        let view = text(&w.provider.view(TITLE));
        assert!(view.contains("Connection Setup"));
        for p in &PROVIDERS {
            assert!(view.contains(p.name), "{}", p.name);
        }
        let mut w = Wizard::new(None);
        w.update(key(KeyCode::Enter));
        let view = text(&w.form.as_ref().unwrap().lines());
        for label in ["Host:", "Port:", "Database (required):"] {
            assert!(view.contains(label), "{label}");
        }
    }

    #[test]
    fn the_focused_field_and_the_error_fit_80_by_24() {
        let labels = [
            "Host:",
            "Port:",
            "Database (required):",
            "Maintenance DB:",
            "Username:",
            "Password:",
        ];
        let mut w = Wizard::new(None);
        w.update(key(KeyCode::Enter));
        for (i, label) in labels.iter().enumerate() {
            assert_eq!(focus(&w), i);
            assert!(screen(&w, 80, 24).contains(label), "{label}");
            w.update(key(KeyCode::Tab));
        }
        let mut w = Wizard::new(None);
        local_with_database(&mut w, "");
        assert!(screen(&w, 80, 24).contains("database name is required"));
        for downs in 0..PROVIDERS.len() {
            let mut w = Wizard::new(None);
            provider(&mut w, downs);
            if w.form.is_none() {
                w.update(key(KeyCode::Enter));
            }
            let view = screen(&w, 80, 24);
            assert!(
                view.contains(w.form.as_ref().unwrap().inputs[0].label),
                "{view}"
            );
        }
    }

    #[test]
    fn an_existing_config_prefills_the_host_form() {
        let initial = Connection {
            host: Some("127.0.0.1".into()),
            port: Some("55602".into()),
            database: Some("trialw1".into()),
            maintenance_database: Some("template1".into()),
            username: Some("app".into()),
            ..Connection::default()
        };
        let mut w = Wizard::new(Some(initial));
        w.update(key(KeyCode::Enter));
        assert_eq!(
            values(&w),
            ["127.0.0.1", "55602", "trialw1", "template1", "app", ""]
        );
    }

    #[test]
    fn existing_tls_is_kept_but_a_token_never_gets_a_plaintext_capable_mode() {
        let initial = |mode: &str| Connection {
            sslmode: Some(mode.into()),
            sslcert: Some("/client.crt".into()),
            sslkey: Some("/client.key".into()),
            sslrootcert: Some("/ca.pem".into()),
            ..Connection::default()
        };
        let with = |auth: Option<Cloud>, mode: &str, kept: Option<&str>| {
            let mut s = Settings {
                auth,
                sslmode: Some(mode.into()),
                ..Settings::default()
            };
            if let Some(k) = kept {
                keep_tls(&mut s, &initial(k));
            }
            s
        };
        let s = with(None, "prefer", Some("verify-full"));
        assert_eq!(
            (
                s.sslmode.as_deref(),
                s.sslrootcert.as_deref(),
                s.sslcert.as_deref(),
                s.sslkey.as_deref()
            ),
            (
                Some("verify-full"),
                Some("/ca.pem"),
                Some("/client.crt"),
                Some("/client.key")
            )
        );
        assert_eq!(
            with(Some(Cloud::Azure), "require", Some("verify-full"))
                .sslmode
                .as_deref(),
            Some("verify-full")
        );
        assert_eq!(
            with(Some(Cloud::Azure), "require", Some("prefer"))
                .sslmode
                .as_deref(),
            Some("require")
        );
        assert_eq!(
            with(Some(Cloud::Aws), "require", Some("disable"))
                .sslmode
                .as_deref(),
            Some("require")
        );
        let mut w = Wizard::new(None);
        let Some(Effect::Test(s)) = local_with_database(&mut w, "d") else {
            panic!("no test");
        };
        assert_eq!(s.sslmode.as_deref(), Some("prefer"));
    }

    #[test]
    fn fields_are_read_by_name_whatever_their_order() {
        let mut w = Wizard::new(None);
        provider(&mut w, 2);
        w.update(key(KeyCode::Enter));
        let form = w.form.as_mut().unwrap();
        form.inputs.reverse();
        for (field, value) in [
            (Field::Host, "h"),
            (Field::Port, "6000"),
            (Field::Database, "d"),
            (Field::Username, "u"),
            (Field::Region, "r"),
        ] {
            let i = form.index(field);
            form.inputs[i].value = value.chars().collect();
        }
        let s = form.settings().unwrap();
        assert_eq!(
            (
                s.host.as_deref(),
                s.port,
                s.database.as_deref(),
                s.username.as_deref(),
                s.aws_region.as_deref()
            ),
            (Some("h"), Some(6000), Some("d"), Some("u"), Some("r"))
        );
        let database = form.index(Field::Database);
        form.inputs[database].value.clear();
        assert_eq!(
            form.settings(),
            Err((Field::Database, "database name is required".into()))
        );
    }
}
