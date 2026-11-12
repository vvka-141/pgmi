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
    type Page: PartialEq + Copy;

    fn update(&mut self, key: KeyEvent) -> Option<connection::Effect<Self::Done>>;
    fn tested(&mut self, result: connection::TestResult);
    fn tick(&mut self);
    /// Changes whenever the screen must be cleared before the next draw.
    fn page(&self) -> Self::Page;
    fn view(&self) -> Paragraph<'static>;
}

pub(crate) fn drive<S: Steps>(
    steps: &mut S,
    env: &Env,
    runtime: &tokio::runtime::Handle,
) -> Result<S::Done> {
    let mut screen = Screen::open()?;
    let (tx, rx) = mpsc::channel();
    let mut page = steps.page();
    loop {
        screen
            .terminal
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
        if steps.page() != page {
            page = steps.page();
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

pub(crate) struct Screen {
    terminal: Terminal<CrosstermBackend<Stdout>>,
    _restore: Restore,
}

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

/// Raw mode is on: dropping this leaves the alternate screen and raw mode,
/// unless the panic hook already did.
#[derive(Debug)]
struct Restore(fn());

impl Drop for Restore {
    fn drop(&mut self) {
        if OPEN.replace(false) {
            (self.0)();
        }
    }
}

fn opened<T>(
    enter: impl FnOnce() -> std::io::Result<()>,
    leave: fn(),
    show: impl FnOnce() -> std::io::Result<T>,
) -> Result<(T, Restore)> {
    enter().map_err(io)?;
    OPEN.set(true);
    let restore = Restore(leave);
    let shown = show().map_err(io)?;
    Ok((shown, restore))
}

impl Screen {
    pub(crate) fn open() -> Result<Self> {
        static HOOK: Once = Once::new();
        HOOK.call_once(|| std::panic::set_hook(restoring(std::panic::take_hook(), leave)));
        let (terminal, restore) = opened(terminal::enable_raw_mode, leave, || {
            let mut out = std::io::stdout();
            execute!(out, EnterAlternateScreen, cursor::Hide)?;
            Terminal::new(CrosstermBackend::new(out))
        })?;
        Ok(Screen {
            terminal,
            _restore: restore,
        })
    }

    pub(crate) fn reset(&mut self) -> Result<()> {
        execute!(std::io::stdout(), terminal::Clear(terminal::ClearType::All)).map_err(io)?;
        self.terminal = Terminal::new(CrosstermBackend::new(std::io::stdout())).map_err(io)?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use std::sync::{Arc, Mutex};

    use super::*;

    #[test]
    fn a_screen_that_fails_to_start_leaves_raw_mode() {
        thread_local! {
            static LEFT: Cell<u8> = const { Cell::new(0) };
        }
        fn left() {
            LEFT.set(LEFT.get() + 1);
        }
        let failed = opened(
            || Ok(()),
            left,
            || -> std::io::Result<()> { Err(std::io::Error::other("no size")) },
        );
        assert_eq!(failed.unwrap_err().kind(), crate::error::Kind::General);
        assert_eq!(LEFT.get(), 1);
        assert!(!OPEN.get());
        let refused = opened(|| Err(std::io::Error::other("not a tty")), left, || Ok(()));
        assert!(refused.is_err());
        assert_eq!(LEFT.get(), 1);
        let ((), restore) = opened(|| Ok(()), left, || Ok(())).unwrap();
        assert_eq!(LEFT.get(), 1);
        drop(restore);
        assert_eq!(LEFT.get(), 2);
    }

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
