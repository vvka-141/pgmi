pub(crate) mod connection;
pub(crate) mod init;
pub(crate) mod select;

use std::io::{BufRead, IsTerminal, Stdout, Write};

use crossterm::terminal::{EnterAlternateScreen, LeaveAlternateScreen};
use crossterm::{cursor, execute, terminal};
use ratatui::Terminal;
use ratatui::backend::CrosstermBackend;

use crate::env::Env;
use crate::error::{Error, Result};

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
