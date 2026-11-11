use crossterm::event::KeyEvent;
use ratatui::widgets::Paragraph;

use super::connection::{self, Effect, Settings};
use super::select::{Outcome, Select};
use crate::env::Env;
use crate::error::{Error, Kind, Result};
use crate::templates::Template;

const TITLE: &str = "pgmi init - Project Setup";

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Done {
    Finished {
        template: Template,
        connection: Option<Box<Settings>>,
    },
    Cancelled,
    Interrupted,
}

/// Template, then "Set up the connection now?", then the connection wizard
/// itself; Esc on the connection wizard's first screen comes back here.
pub(crate) struct Wizard {
    template: Select,
    setup: Option<Select>,
    connection: Option<connection::Wizard>,
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
            setup: None,
            connection: None,
        }
    }

    fn template(&self) -> Template {
        Template::ALL[self.template.cursor]
    }

    pub(crate) fn update(&mut self, key: KeyEvent) -> Option<Effect<Done>> {
        if let Some(c) = &mut self.connection {
            return match c.update(key)? {
                Effect::Test(s) => Some(Effect::Test(s)),
                Effect::Done(connection::Done::Finished(s)) => Some(Effect::Done(Done::Finished {
                    template: self.template(),
                    connection: Some(s),
                })),
                Effect::Done(connection::Done::Cancelled) => {
                    self.connection = None;
                    None
                }
                Effect::Done(connection::Done::Interrupted) => {
                    Some(Effect::Done(Done::Interrupted))
                }
            };
        }
        if let Some(c) = &mut self.setup {
            return match c.update(key)? {
                Outcome::Picked(1) => {
                    self.connection = Some(connection::Wizard::new(None));
                    None
                }
                Outcome::Picked(_) => Some(Effect::Done(Done::Finished {
                    template: self.template(),
                    connection: None,
                })),
                Outcome::Back => {
                    self.setup = None;
                    None
                }
                Outcome::Cancel => Some(Effect::Done(Done::Cancelled)),
            };
        }
        match self.template.update(key)? {
            Outcome::Picked(_) => {
                self.setup = Some(Select {
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
            Outcome::Back | Outcome::Cancel => Some(Effect::Done(Done::Cancelled)),
        }
    }
}

impl super::Steps for Wizard {
    type Done = Done;

    fn update(&mut self, key: KeyEvent) -> Option<Effect<Done>> {
        Wizard::update(self, key)
    }

    fn tested(&mut self, result: std::result::Result<String, String>) {
        if let Some(c) = &mut self.connection {
            c.tested(result);
        }
    }

    fn tick(&mut self) {
        if let Some(c) = &mut self.connection {
            c.tick();
        }
    }

    fn screen(&self) -> (u8, u8, u8) {
        match &self.connection {
            Some(c) => {
                let (stage, test) = c.page();
                (2, stage, test)
            }
            None => (u8::from(self.setup.is_some()), 0, 0),
        }
    }

    fn view(&self) -> Paragraph<'static> {
        match (&self.connection, &self.setup) {
            (Some(c), _) => c.view(),
            (None, Some(s)) => Paragraph::new(s.view(TITLE)),
            (None, None) => Paragraph::new(self.template.view(TITLE)),
        }
    }
}

pub(crate) fn run(target: &str, env: &Env, runtime: &tokio::runtime::Handle) -> Result<Done> {
    super::drive(&mut Wizard::new(target), env, runtime)
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

    fn done(d: Done) -> Option<Effect<Done>> {
        Some(Effect::Done(d))
    }

    #[test]
    fn down_enter_enter_picks_basic_without_connection() {
        let mut w = Wizard::new("/tmp/x");
        assert_eq!(w.update(key(KeyCode::Down)), None);
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert_eq!(
            w.update(key(KeyCode::Enter)),
            done(Done::Finished {
                template: Template::Basic,
                connection: None
            })
        );
    }

    #[test]
    fn esc_goes_back_then_cancels() {
        let mut w = Wizard::new("/tmp/x");
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert_eq!(w.update(key(KeyCode::Esc)), None);
        assert_eq!(w.update(key(KeyCode::Esc)), done(Done::Cancelled));
    }

    fn texts(w: &Wizard) -> Vec<String> {
        let lines = match &w.setup {
            Some(c) => c.view(TITLE),
            None => w.template.view(TITLE),
        };
        lines
            .iter()
            .map(|l| l.spans.iter().map(|s| s.content.as_ref()).collect())
            .collect()
    }

    fn into_connection_wizard() -> Wizard {
        let mut w = Wizard::new("/tmp/x");
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert_eq!(w.update(key(KeyCode::Down)), None);
        assert_eq!(w.update(key(KeyCode::Enter)), None);
        assert!(w.connection.is_some());
        w
    }

    #[test]
    fn yes_runs_the_connection_wizard_and_finishes_with_its_settings() {
        let mut w = into_connection_wizard();
        assert_eq!(super::super::Steps::screen(&w), (2, 0, 0));
        w.update(key(KeyCode::Enter));
        w.update(key(KeyCode::Enter));
        w.update(key(KeyCode::Enter));
        for c in "app".chars() {
            w.update(key(KeyCode::Char(c)));
        }
        for _ in 0..3 {
            w.update(key(KeyCode::Enter));
        }
        let Some(Effect::Test(s)) = w.update(key(KeyCode::Enter)) else {
            panic!("no test");
        };
        assert_eq!(s.database.as_deref(), Some("app"));
        super::super::Steps::tested(&mut w, Ok("PostgreSQL 18".into()));
        assert_eq!(
            w.update(key(KeyCode::Enter)),
            done(Done::Finished {
                template: Template::ALL[0],
                connection: Some(s)
            })
        );
    }

    #[test]
    fn esc_on_the_connection_wizard_returns_to_the_setup_question() {
        let mut w = into_connection_wizard();
        assert_eq!(w.update(key(KeyCode::Esc)), None);
        assert!(w.connection.is_none());
        assert!(
            texts(&w)
                .iter()
                .any(|l| l.contains("Set up the connection now?"))
        );
        assert_eq!(super::super::Steps::screen(&w), (1, 0, 0));
        assert_eq!(w.update(key(KeyCode::Up)), None);
        assert_eq!(
            w.update(key(KeyCode::Enter)),
            done(Done::Finished {
                template: Template::ALL[0],
                connection: None
            })
        );
    }

    #[test]
    fn ctrl_c_stops_init_on_every_screen() {
        let ctrl_c = KeyEvent::new(KeyCode::Char('c'), KeyModifiers::CONTROL);
        assert_eq!(Wizard::new("/tmp/x").update(ctrl_c), done(Done::Cancelled));
        let mut w = Wizard::new("/tmp/x");
        w.update(key(KeyCode::Enter));
        assert_eq!(w.update(ctrl_c), done(Done::Cancelled));
        assert_eq!(cancelled().kind(), Kind::Interrupted);
        assert_eq!(
            into_connection_wizard().update(ctrl_c),
            done(Done::Interrupted)
        );
    }

    #[test]
    fn the_template_screen_lists_every_template_with_aligned_radios() {
        let mut w = Wizard::new("/tmp/x");
        for _ in 0..2 {
            let lines = texts(&w);
            for t in Template::ALL {
                if w.setup.is_none() {
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
