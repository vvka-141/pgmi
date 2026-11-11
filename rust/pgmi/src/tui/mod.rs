pub(crate) mod connection;
pub(crate) mod init;
pub(crate) mod select;

use std::io::{BufRead, IsTerminal, Stdout, Write};
use std::sync::mpsc;
use std::time::Duration;

use crossterm::event::{Event, KeyEvent};
use crossterm::terminal::{EnterAlternateScreen, LeaveAlternateScreen};
use crossterm::{cursor, execute, terminal};
use ratatui::Terminal;
use ratatui::backend::CrosstermBackend;
use ratatui::widgets::Paragraph;

use crate::env::Env;
use crate::error::{Error, Result};

/// A wizard the event loop in `drive` can run: keys in, a connection test
/// or a result out.
pub(crate) trait Steps {
    type Done;

    fn update(&mut self, key: KeyEvent) -> Option<connection::Effect<Self::Done>>;
    fn tested(&mut self, result: std::result::Result<String, String>);
    fn tick(&mut self);
    /// Changes whenever the screen must be cleared before the next draw.
    fn screen(&self) -> (u8, u8, u8);
    fn view(&self) -> Paragraph<'static>;
}

pub(crate) fn drive<S: Steps>(
    steps: &mut S,
    env: &Env,
    runtime: &tokio::runtime::Handle,
) -> Result<S::Done> {
    let mut screen = Screen::open()?;
    let (tx, rx) = mpsc::channel();
    let mut page = steps.screen();
    loop {
        screen
            .0
            .draw(|f| f.render_widget(steps.view(), f.area()))
            .map_err(io)?;
        if let Ok(result) = rx.try_recv() {
            steps.tested(result);
        } else if crossterm::event::poll(Duration::from_millis(100)).map_err(io)? {
            if let Event::Key(key) = crossterm::event::read().map_err(io)? {
                match steps.update(key) {
                    Some(connection::Effect::Done(done)) => return Ok(done),
                    Some(connection::Effect::Test(s)) => {
                        let tx = tx.clone();
                        let env = env.clone();
                        runtime.spawn(async move {
                            let _ = tx.send(connection::test(*s, env).await);
                        });
                    }
                    None => {}
                }
            }
        } else {
            steps.tick();
        }
        if steps.screen() != page {
            page = steps.screen();
            screen.reset()?;
        }
    }
}

pub(crate) fn interactive(env: &Env) -> bool {
    std::io::stdin().is_terminal()
        && std::io::stdout().is_terminal()
        && env.non_empty("CI").ok().flatten().is_none()
        && env.get("PGMI_NON_INTERACTIVE").ok().flatten() != Some("1")
}

pub(crate) fn confirm(message: &str) -> bool {
    let _ = write!(std::io::stderr(), "{message} [Y/n]: ");
    let mut line = String::new();
    match std::io::stdin().lock().read_line(&mut line) {
        Ok(0) | Err(_) => false,
        Ok(_) => matches!(line.trim().to_ascii_lowercase().as_str(), "" | "y" | "yes"),
    }
}

pub(crate) fn io(e: std::io::Error) -> Error {
    Error::general("terminal").with_source(e)
}

pub(crate) struct Screen(pub Terminal<CrosstermBackend<Stdout>>);

impl Screen {
    pub(crate) fn open() -> Result<Self> {
        terminal::enable_raw_mode().map_err(io)?;
        let mut out = std::io::stdout();
        if let Err(e) = execute!(out, EnterAlternateScreen, cursor::Hide) {
            let _ = terminal::disable_raw_mode();
            return Err(io(e));
        }
        Terminal::new(CrosstermBackend::new(out))
            .map(Self)
            .map_err(io)
    }

    pub(crate) fn reset(&mut self) -> Result<()> {
        execute!(std::io::stdout(), terminal::Clear(terminal::ClearType::All)).map_err(io)?;
        self.0 = Terminal::new(CrosstermBackend::new(std::io::stdout())).map_err(io)?;
        Ok(())
    }
}

impl Drop for Screen {
    fn drop(&mut self) {
        let _ = execute!(self.0.backend_mut(), LeaveAlternateScreen, cursor::Show);
        let _ = terminal::disable_raw_mode();
    }
}
