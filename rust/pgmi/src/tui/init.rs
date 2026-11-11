use crossterm::event::{Event, KeyEvent};
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

pub fn run(target: &str) -> Result<Done> {
    let mut wizard = Wizard::new(target);
    let mut screen = super::Screen::open()?;
    loop {
        screen
            .0
            .draw(|f| f.render_widget(wizard.view(), f.area()))
            .map_err(super::io)?;
        if let Event::Key(key) = crossterm::event::read().map_err(super::io)? {
            let stage = wizard.connection.is_some();
            if let Some(done) = wizard.update(key) {
                return Ok(done);
            }
            if stage != wizard.connection.is_some() {
                screen.reset()?;
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
