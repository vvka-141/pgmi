pub(crate) mod connection;
pub(crate) mod init;
pub(crate) mod select;

use std::cell::Cell;
use std::io::{BufRead, IsTerminal, Stdout, Write};
use std::panic::PanicHookInfo;
use std::sync::{Once, mpsc};
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
    fn tested(&mut self, result: connection::TestResult);
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

thread_local! {
    static OPEN: Cell<bool> = const { Cell::new(false) };
}

type Hook = Box<dyn Fn(&PanicHookInfo<'_>) + Sync + Send + 'static>;

fn leave() {
    let _ = execute!(std::io::stdout(), LeaveAlternateScreen, cursor::Show);
    let _ = terminal::disable_raw_mode();
}

fn restoring(previous: Hook, leave: impl Fn() + Sync + Send + 'static) -> Hook {
    Box::new(move |info| {
        if OPEN.replace(false) {
            leave();
        }
        previous(info);
    })
}

impl Screen {
    pub(crate) fn open() -> Result<Self> {
        static HOOK: Once = Once::new();
        HOOK.call_once(|| std::panic::set_hook(restoring(std::panic::take_hook(), leave)));
        terminal::enable_raw_mode().map_err(io)?;
        let mut out = std::io::stdout();
        if let Err(e) = execute!(out, EnterAlternateScreen, cursor::Hide) {
            let _ = terminal::disable_raw_mode();
            return Err(io(e));
        }
        OPEN.set(true);
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
        if OPEN.replace(false) {
            leave();
        }
    }
}

#[cfg(test)]
mod tests {
    use std::sync::{Arc, Mutex};

    use super::*;

    #[test]
    fn a_panic_leaves_the_screen_before_its_message_prints() {
        static SEEN: Mutex<Vec<&str>> = Mutex::new(Vec::new());
        thread_local! {
            static MINE: Cell<bool> = const { Cell::new(false) };
        }
        let saved: Arc<Hook> = Arc::new(std::panic::take_hook());
        let inner = saved.clone();
        std::panic::set_hook(Box::new(move |info| {
            if MINE.get() {
                SEEN.lock().unwrap().push("message");
            } else {
                inner(info);
            }
        }));
        std::panic::set_hook(restoring(std::panic::take_hook(), || {
            if MINE.get() {
                SEEN.lock().unwrap().push("leave");
            }
        }));
        let outcome = std::thread::spawn(|| {
            MINE.set(true);
            OPEN.set(true);
            std::panic::panic_any("in the wizard");
        })
        .join();
        drop(std::panic::take_hook());
        std::panic::set_hook(Box::new(move |info| saved(info)));
        assert!(outcome.is_err());
        assert_eq!(*SEEN.lock().unwrap(), ["leave", "message"]);
    }
}
