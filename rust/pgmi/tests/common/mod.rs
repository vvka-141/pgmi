#![cfg(test)]
#![allow(dead_code)]

use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::str::FromStr;
use std::sync::atomic::{AtomicUsize, Ordering};

use tokio::runtime::Runtime;
use tokio_postgres::types::{FromSqlOwned, ToSql};
use tokio_postgres::{Config, NoTls, Row};

fn var(name: &str) -> Option<String> {
    std::env::vars_os()
        .find(|(k, _)| k == name)
        .and_then(|(_, v)| v.into_string().ok())
        .filter(|v| !v.is_empty())
}

pub struct Pg {
    conn: String,
}

pub fn pg() -> Option<Pg> {
    match var("PGMI_TEST_CONN") {
        Some(conn) => Some(Pg { conn }),
        None if var("PGMI_REQUIRE_DB").is_some() => {
            panic!("PGMI_REQUIRE_DB is set but PGMI_TEST_CONN is not")
        }
        None => {
            eprintln!("skipped: PGMI_TEST_CONN is not set");
            None
        }
    }
}

impl Pg {
    pub fn conn(&self) -> &str {
        &self.conn
    }

    pub fn admin(&self) -> Client {
        Client::connect(&self.conn, None)
    }

    pub fn client_for(&self, database: &str) -> Client {
        Client::connect(&self.conn, Some(database))
    }

    pub fn database(&self, name: &str) -> Db {
        let db = Db {
            conn: self.conn.clone(),
            name: name.to_string(),
        };
        db.drop_database();
        db
    }

    pub fn deploy(&self, project: &Project, db: &Db) -> Deploy<'_> {
        Deploy {
            pg: self,
            project: project.path().to_path_buf(),
            db: db.name.clone(),
            params: Vec::new(),
            args: Vec::new(),
            conn: None,
            env: Vec::new(),
        }
    }
}

pub struct Db {
    conn: String,
    name: String,
}

impl Db {
    pub fn name(&self) -> &str {
        &self.name
    }

    pub fn client(&self) -> Client {
        Client::connect(&self.conn, Some(&self.name))
    }

    pub fn client_as(&self, user: &str, password: &str) -> Client {
        let mut config = Config::from_str(&self.conn).expect("PGMI_TEST_CONN");
        config.dbname(&self.name).user(user).password(password);
        Client::from_config(&config)
    }

    pub fn url_as(&self, user: &str, password: &str) -> String {
        let config = Config::from_str(&self.conn).expect("PGMI_TEST_CONN");
        let (host, port) = endpoint(&config);
        format!("postgresql://{user}:{password}@{host}:{port}/{}", self.name)
    }

    fn drop_database(&self) {
        let admin = Client::connect(&self.conn, None);
        admin
            .batch(&format!(
                "DROP DATABASE IF EXISTS {} WITH (FORCE)",
                ident(&self.name)
            ))
            .expect("drop test database");
    }
}

impl Drop for Db {
    fn drop(&mut self) {
        if !std::thread::panicking() {
            self.drop_database();
        }
    }
}

fn endpoint(config: &Config) -> (String, u16) {
    let host = match config.get_hosts().first() {
        Some(tokio_postgres::config::Host::Tcp(h)) => h.clone(),
        _ => panic!("PGMI_TEST_CONN must name a TCP host"),
    };
    (host, config.get_ports().first().copied().unwrap_or(5432))
}

pub fn ident(s: &str) -> String {
    format!("\"{}\"", s.replace('"', "\"\""))
}

pub struct Client {
    rt: Runtime,
    client: tokio_postgres::Client,
    notices: std::sync::Arc<std::sync::Mutex<Vec<String>>>,
}

impl Client {
    fn connect(conn: &str, db: Option<&str>) -> Client {
        let mut config = Config::from_str(conn).expect("PGMI_TEST_CONN");
        if let Some(db) = db {
            config.dbname(db);
        }
        Client::from_config(&config)
    }

    fn from_config(config: &Config) -> Client {
        Client::try_from_config(config).expect("connect to the test server")
    }

    pub fn try_connect(
        db: &Db,
        user: &str,
        password: &str,
    ) -> Result<Client, tokio_postgres::Error> {
        let mut config = Config::from_str(&db.conn).expect("PGMI_TEST_CONN");
        config.dbname(&db.name).user(user).password(password);
        Client::try_from_config(&config)
    }

    fn try_from_config(config: &Config) -> Result<Client, tokio_postgres::Error> {
        use futures_util::StreamExt;
        let rt = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .expect("runtime");
        let notices = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
        let sink = notices.clone();
        let client = rt.block_on(async {
            let stream = tokio::net::TcpStream::connect(endpoint(config))
                .await
                .expect("reach the test server over TCP");
            let (client, mut connection) = config.connect_raw(stream, NoTls).await?;
            let mut messages = futures_util::stream::poll_fn(move |cx| connection.poll_message(cx));
            tokio::spawn(async move {
                while let Some(Ok(m)) = messages.next().await {
                    if let tokio_postgres::AsyncMessage::Notice(n) = m {
                        sink.lock().expect("notices").push(n.message().to_string());
                    }
                }
            });
            Ok::<_, tokio_postgres::Error>(client)
        })?;
        Ok(Client {
            rt,
            client,
            notices,
        })
    }

    pub fn batch(&self, sql: &str) -> Result<(), tokio_postgres::Error> {
        self.rt.block_on(self.client.batch_execute(sql))
    }

    pub fn exec(&self, sql: &str) {
        if let Err(e) = self.batch(sql) {
            panic!("{}\n{sql}", describe(&e));
        }
    }

    pub fn try_query(
        &self,
        sql: &str,
        params: &[&(dyn ToSql + Sync)],
    ) -> Result<Vec<Row>, tokio_postgres::Error> {
        self.rt.block_on(self.client.query(sql, params))
    }

    pub fn query(&self, sql: &str, params: &[&(dyn ToSql + Sync)]) -> Vec<Row> {
        self.try_query(sql, params)
            .unwrap_or_else(|e| panic!("{}\n{sql}", describe(&e)))
    }

    pub fn one<T: FromSqlOwned>(&self, sql: &str, params: &[&(dyn ToSql + Sync)]) -> T {
        let rows = self.query(sql, params);
        assert_eq!(rows.len(), 1, "{sql} returned {} rows", rows.len());
        rows[0].get(0)
    }

    pub fn notices(&self) -> Vec<String> {
        self.notices.lock().expect("notices").clone()
    }
}

pub fn describe(e: &tokio_postgres::Error) -> String {
    match e.as_db_error() {
        Some(d) => format!("{} {}: {}", d.severity(), d.code().code(), d.message()),
        None => e.to_string(),
    }
}

pub fn sqlstate(e: &tokio_postgres::Error) -> Option<String> {
    e.code().map(|c| c.code().to_string())
}

static NEXT: AtomicUsize = AtomicUsize::new(0);

pub struct Project {
    dir: PathBuf,
}

impl Project {
    pub fn empty() -> Project {
        let dir = std::env::temp_dir().join(format!(
            "pgmi-rs-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).expect("create project directory");
        Project { dir }
    }

    pub fn init(template: &str) -> Project {
        let p = Project::empty();
        std::fs::remove_dir(&p.dir).expect("init wants a missing directory");
        let out = Command::new(env!("CARGO_BIN_EXE_pgmi"))
            .args(["init", &p.dir.display().to_string(), "--template", template])
            .env_clear()
            .env("PGMI_NO_BANNER", "1")
            .output()
            .expect("run pgmi init");
        assert!(
            out.status.success(),
            "pgmi init --template {template}: {}",
            String::from_utf8_lossy(&out.stderr)
        );
        p
    }

    pub fn path(&self) -> &Path {
        &self.dir
    }

    pub fn file(&self, rel: &str) -> PathBuf {
        self.dir.join(rel)
    }

    pub fn read(&self, rel: &str) -> String {
        std::fs::read_to_string(self.file(rel)).unwrap_or_else(|e| panic!("read {rel}: {e}"))
    }

    pub fn write(&self, rel: &str, content: &str) -> &Self {
        let path = self.file(rel);
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent).expect("create parent");
        }
        std::fs::write(&path, content).unwrap_or_else(|e| panic!("write {rel}: {e}"));
        self
    }

    pub fn remove(&self, rel: &str) -> &Self {
        let path = self.file(rel);
        let meta = std::fs::metadata(&path).unwrap_or_else(|e| panic!("{rel}: {e}"));
        if meta.is_dir() {
            std::fs::remove_dir_all(&path)
        } else {
            std::fs::remove_file(&path)
        }
        .unwrap_or_else(|e| panic!("remove {rel}: {e}"));
        self
    }

    pub fn replace(&self, rel: &str, from: &str, to: &str) -> &Self {
        let text = self.read(rel);
        assert_eq!(
            text.matches(from).count(),
            1,
            "{rel}: {from:?} must occur exactly once"
        );
        self.write(rel, &text.replacen(from, to, 1))
    }
}

impl Drop for Project {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

pub struct Deploy<'a> {
    pg: &'a Pg,
    project: PathBuf,
    db: String,
    params: Vec<(String, String)>,
    args: Vec<String>,
    conn: Option<String>,
    env: Vec<(String, String)>,
}

pub struct Outcome {
    pub code: i32,
    pub stdout: String,
    pub stderr: String,
}

impl Outcome {
    pub fn lines(&self) -> Vec<&str> {
        self.stderr.lines().collect()
    }

    pub fn ok(self) -> Outcome {
        assert_eq!(self.code, 0, "deploy failed:\n{}", self.stderr);
        self
    }

    pub fn fails(self, code: i32) -> Outcome {
        assert_eq!(self.code, code, "deploy exit code:\n{}", self.stderr);
        self
    }

    pub fn says(&self, text: &str) -> &Self {
        assert!(
            self.stderr.contains(text),
            "deploy output lacks {text:?}:\n{}",
            self.stderr
        );
        self
    }

    pub fn never_says(&self, text: &str) -> &Self {
        assert!(
            !self.stderr.contains(text) && !self.stdout.contains(text),
            "deploy output contains {text:?}:\n{}{}",
            self.stdout,
            self.stderr
        );
        self
    }

    pub fn record(&self) -> serde_json::Value {
        serde_json::from_str(&self.stdout)
            .unwrap_or_else(|e| panic!("--json record: {e}\n{}", self.stdout))
    }
}

impl Deploy<'_> {
    pub fn param(mut self, key: &str, value: &str) -> Self {
        self.params.push((key.to_string(), value.to_string()));
        self
    }

    pub fn params(mut self, pairs: &[(&str, &str)]) -> Self {
        for (k, v) in pairs {
            self.params.push((k.to_string(), v.to_string()));
        }
        self
    }

    pub fn overwrite(mut self) -> Self {
        self.args.extend(["--overwrite".into(), "--force".into()]);
        self
    }

    pub fn env(mut self, key: &str, value: &str) -> Self {
        self.env.push((key.to_string(), value.to_string()));
        self
    }

    pub fn arg(mut self, a: &str) -> Self {
        self.args.push(a.to_string());
        self
    }

    pub fn connection(mut self, url: &str) -> Self {
        self.conn = Some(url.to_string());
        self
    }

    pub fn run(self) -> Outcome {
        let conn = self.conn.as_deref().unwrap_or(&self.pg.conn).to_string();
        let mut cmd = Command::new(env!("CARGO_BIN_EXE_pgmi"));
        cmd.arg("deploy")
            .arg(&self.project)
            .args(["--connection", &conn, "-d", &self.db])
            .args(&self.args)
            .env_clear()
            .env("PGMI_NO_BANNER", "1")
            .envs(self.env.iter().map(|(k, v)| (k, v)))
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        if !self.params.is_empty() {
            cmd.args(["--params-file", "-"]);
        }
        let mut child = cmd.spawn().expect("run pgmi deploy");
        let mut stdin = child.stdin.take().expect("stdin");
        for (k, v) in &self.params {
            writeln!(stdin, "{k}={v}").expect("write params");
        }
        drop(stdin);
        let out = child.wait_with_output().expect("pgmi deploy output");
        Outcome {
            code: out.status.code().unwrap_or(-1),
            stdout: String::from_utf8_lossy(&out.stdout).into_owned(),
            stderr: String::from_utf8_lossy(&out.stderr).into_owned(),
        }
    }
}
