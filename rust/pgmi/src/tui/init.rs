use crossterm::event::{Event, KeyEvent};
use ratatui::widgets::Paragraph;

use super::select::{Outcome, Select};
use crate::error::{Error, Kind, Result};
use crate::templates::Template;

const TITLE: &str = "pgmi init - Project Setup";

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Done {
    Finished { template: Template, connect: bool },
    Cancelled,
}

pub(crate) struct Wizard {
    template: Select,
    connection: Option<Select>,
}

impl Wizard {
    pub(crate) fn new(target: &str) -> Self {
        Self {
            template: Select {
                prompt: format!("Select a template  →  {target}"),
                choices: Template::ALL
                    .iter()
                    .map(|t| (t.name().to_string(), t.summary().to_string()))
                    .collect(),
                help: "↑/↓ navigate • enter select • esc back",
                cursor: 0,
            },
            connection: None,
        }
    }

    pub(crate) fn update(&mut self, key: KeyEvent) -> Option<Done> {
        if let Some(c) = &mut self.connection {
            return match c.update(key)? {
                Outcome::Picked(i) => Some(Done::Finished {
                    template: Template::ALL[self.template.cursor],
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

    pub(crate) fn view(&self) -> Paragraph<'static> {
        let lines = match &self.connection {
            Some(c) => c.view(TITLE),
            None => self.template.view(TITLE),
        };
        Paragraph::new(lines)
    }
}

pub(crate) fn run(target: &str) -> Result<Done> {
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

pub(crate) fn cancelled() -> Error {
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
                template: Template::Basic,
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

    fn texts(w: &Wizard) -> Vec<String> {
        let lines = match &w.connection {
            Some(c) => c.view(TITLE),
            None => w.template.view(TITLE),
        };
        lines
            .iter()
            .map(|l| l.spans.iter().map(|s| s.content.as_ref()).collect())
            .collect()
    }

    #[test]
    fn yes_asks_for_the_connection_wizard() {
        let mut w = Wizard::new("/tmp/x");
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert_eq!(w.update(key(KeyCode::Down)), None);
        assert_eq!(
            w.update(key(KeyCode::Enter)),
            Some(Done::Finished {
                template: Template::ALL[0],
                connect: true
            })
        );
    }

    #[test]
    fn ctrl_c_stops_init_on_either_screen() {
        let ctrl_c = KeyEvent::new(KeyCode::Char('c'), KeyModifiers::CONTROL);
        assert_eq!(Wizard::new("/tmp/x").update(ctrl_c), Some(Done::Cancelled));
        let mut w = Wizard::new("/tmp/x");
        w.update(key(KeyCode::Enter));
        assert_eq!(w.update(ctrl_c), Some(Done::Cancelled));
        assert_eq!(cancelled().kind(), Kind::Interrupted);
    }

    #[test]
    fn the_template_screen_lists_every_template_with_aligned_radios() {
        let mut w = Wizard::new("/tmp/x");
        for _ in 0..2 {
            let lines = texts(&w);
            for t in Template::ALL {
                if w.connection.is_none() {
                    assert!(lines.iter().any(|l| l.contains(t.name())), "{}", t.name());
                }
            }
            let cols: std::collections::BTreeSet<usize> = lines
                .iter()
                .filter_map(|l| l.find(['●', '○']).map(|i| l[..i].chars().count()))
                .collect();
            assert_eq!(cols.len(), 1, "{lines:?}");
            w.update(key(KeyCode::Enter));
        }
    }
}
