use std::io::Stdout;

use crossterm::event::{Event, KeyEvent};
use crossterm::terminal::{EnterAlternateScreen, LeaveAlternateScreen};
use crossterm::{cursor, execute, terminal};
use ratatui::Terminal;
use ratatui::backend::CrosstermBackend;
use ratatui::widgets::Paragraph;

use super::select::{Outcome, Select};
use crate::commands::templates::LIST;
use crate::error::{Error, Kind, Result};

const TITLE: &str = "pgmi init - Project Setup";

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Done {
    Finished {
        template: &'static str,
        connect: bool,
    },
    Cancelled,
}

pub struct Wizard {
    template: Select,
    connection: Option<Select>,
}

impl Wizard {
    pub fn new(target: &str) -> Self {
        Self {
            template: Select {
                prompt: format!("Select a template  →  {target}"),
                choices: LIST
                    .iter()
                    .map(|(n, s, _)| (n.to_string(), s.to_string()))
                    .collect(),
                help: "↑/↓ navigate • enter select • esc back",
                cursor: 0,
            },
            connection: None,
        }
    }

    pub fn update(&mut self, key: KeyEvent) -> Option<Done> {
        if let Some(c) = &mut self.connection {
            return match c.update(key)? {
                Outcome::Picked(i) => Some(Done::Finished {
                    template: LIST[self.template.cursor].0,
                    connect: i == 1,
                }),
                Outcome::Back => {
                    self.connection = None;
                    None
                }
                Outcome::Cancel => Some(Done::Cancelled),
            };
        }
        match self.template.update(key)? {
            Outcome::Picked(_) => {
                self.connection = Some(Select {
                    prompt: "Set up the connection now?".into(),
                    choices: vec![
                        (
                            "No — configure later".into(),
                            "pgmi.yaml gets a placeholder".into(),
                        ),
                        (
                            "Yes — run the connection wizard".into(),
                            "writes connection settings to pgmi.yaml".into(),
                        ),
                    ],
                    help: "↑/↓ toggle • enter select • esc back",
                    cursor: 0,
                });
                None
            }
            Outcome::Back | Outcome::Cancel => Some(Done::Cancelled),
        }
    }

    pub fn view(&self) -> Paragraph<'static> {
        let lines = match &self.connection {
            Some(c) => c.view(TITLE),
            None => self.template.view(TITLE),
        };
        Paragraph::new(lines)
    }
}

struct Screen(Terminal<CrosstermBackend<Stdout>>);

impl Screen {
    fn open() -> std::io::Result<Self> {
        terminal::enable_raw_mode()?;
        let mut out = std::io::stdout();
        if let Err(e) = execute!(out, EnterAlternateScreen, cursor::Hide) {
            let _ = terminal::disable_raw_mode();
            return Err(e);
        }
        Terminal::new(CrosstermBackend::new(out)).map(Self)
    }
}

impl Drop for Screen {
    fn drop(&mut self) {
        let _ = execute!(self.0.backend_mut(), LeaveAlternateScreen, cursor::Show);
        let _ = terminal::disable_raw_mode();
    }
}

pub fn run(target: &str) -> Result<Done> {
    let io = |e: std::io::Error| Error::general("terminal").with_source(e);
    let mut wizard = Wizard::new(target);
    let mut screen = Screen::open().map_err(io)?;
    loop {
        screen
            .0
            .draw(|f| f.render_widget(wizard.view(), f.area()))
            .map_err(io)?;
        if let Event::Key(key) = crossterm::event::read().map_err(io)? {
            let stage = wizard.connection.is_some();
            if let Some(done) = wizard.update(key) {
                return Ok(done);
            }
            if stage != wizard.connection.is_some() {
                execute!(std::io::stdout(), terminal::Clear(terminal::ClearType::All))
                    .map_err(io)?;
                screen.0 = Terminal::new(CrosstermBackend::new(std::io::stdout())).map_err(io)?;
            }
        }
    }
}

pub fn cancelled() -> Error {
    Error::new(Kind::Interrupted, "init cancelled")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crossterm::event::{KeyCode, KeyModifiers};

    fn key(code: KeyCode) -> KeyEvent {
        KeyEvent::new(code, KeyModifiers::NONE)
    }

    #[test]
    fn down_enter_enter_picks_basic_without_connection() {
        let mut w = Wizard::new("/tmp/x");
        assert_eq!(w.update(key(KeyCode::Down)), None);
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert_eq!(
            w.update(key(KeyCode::Enter)),
            Some(Done::Finished {
                template: "basic",
                connect: false
            })
        );
    }

    #[test]
    fn esc_goes_back_then_cancels() {
        let mut w = Wizard::new("/tmp/x");
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert_eq!(w.update(key(KeyCode::Esc)), None);
        assert_eq!(w.update(key(KeyCode::Esc)), Some(Done::Cancelled));
    }
}
