use crossterm::event::KeyEvent;
use ratatui::widgets::Paragraph;

use super::connection::{self, Effect, Settings, TestResult};
use super::select::{Choice, Outcome, Select};
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

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Now {
    Later,
    Wizard,
}

/// The screens after a template is picked: "Set up the connection now?",
/// then the connection wizard itself.
struct Setup {
    template: Template,
    now: Select<Now>,
    connection: Option<connection::Wizard>,
}

/// Which screen init shows; the screen is cleared when it changes.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Page {
    Template,
    Setup,
    Connection(connection::Page),
}

/// Template, then [`Setup`]; Esc on the connection wizard's first screen
/// comes back to the question.
pub(crate) struct Wizard {
    template: Select<Template>,
    setup: Option<Setup>,
}

impl Wizard {
    pub(crate) fn new(target: &str) -> Self {
        Self {
            template: Select::new(
                format!("Select a template  →  {target}"),
                Template::ALL
                    .iter()
                    .map(|t| Choice::new(*t, t.name(), t.summary()))
                    .collect(),
                "↑/↓ navigate • enter select • esc back",
            ),
            setup: None,
        }
    }

    fn connection(&self) -> Option<&connection::Wizard> {
        self.setup.as_ref()?.connection.as_ref()
    }

    fn connection_mut(&mut self) -> Option<&mut connection::Wizard> {
        self.setup.as_mut()?.connection.as_mut()
    }

    pub(crate) fn update(&mut self, key: KeyEvent) -> Option<Effect<Done>> {
        if let Some(setup) = &mut self.setup {
            if let Some(c) = &mut setup.connection {
                return match c.update(key)? {
                    Effect::Test(s) => Some(Effect::Test(s)),
                    Effect::Done(connection::Done::Finished(s)) => {
                        Some(Effect::Done(Done::Finished {
                            template: setup.template,
                            connection: Some(s),
                        }))
                    }
                    Effect::Done(connection::Done::Cancelled) => {
                        setup.connection = None;
                        None
                    }
                    Effect::Done(connection::Done::Interrupted) => {
                        Some(Effect::Done(Done::Interrupted))
                    }
                };
            }
            return match setup.now.update(key)? {
                Outcome::Picked(Now::Wizard) => {
                    setup.connection = Some(connection::Wizard::new(None));
                    None
                }
                Outcome::Picked(Now::Later) => Some(Effect::Done(Done::Finished {
                    template: setup.template,
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
            Outcome::Picked(template) => {
                self.setup = Some(Setup {
                    template,
                    now: Select::new(
                        "Set up the connection now?",
                        vec![
                            Choice::new(
                                Now::Later,
                                "No — configure later",
                                "pgmi.yaml gets a placeholder",
                            ),
                            Choice::new(
                                Now::Wizard,
                                "Yes — run the connection wizard",
                                "writes connection settings to pgmi.yaml",
                            ),
                        ],
                        "↑/↓ toggle • enter select • esc back",
                    ),
                    connection: None,
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

    fn tested(&mut self, result: TestResult) {
        if let Some(c) = self.connection_mut() {
            c.tested(result);
        }
    }

    fn tick(&mut self) {
        if let Some(c) = self.connection_mut() {
            c.tick();
        }
    }

    type Page = Page;

    fn page(&self) -> Page {
        match (&self.setup, self.connection()) {
            (_, Some(c)) => Page::Connection(c.page()),
            (Some(_), None) => Page::Setup,
            (None, None) => Page::Template,
        }
    }

    fn view(&self) -> Paragraph<'static> {
        match &self.setup {
            Some(Setup {
                connection: Some(c),
                ..
            }) => c.view(),
            Some(s) => Paragraph::new(s.now.view(TITLE)),
            None => Paragraph::new(self.template.view(TITLE)),
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
            Some(c) => c.now.view(TITLE),
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
        assert!(w.connection().is_some());
        w
    }

    #[test]
    fn yes_runs_the_connection_wizard_and_finishes_with_its_settings() {
        let mut w = into_connection_wizard();
        assert_eq!(
            super::super::Steps::page(&w),
            Page::Connection(connection::Page::Provider)
        );
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
        assert!(w.connection().is_none());
        assert!(
            texts(&w)
                .iter()
                .any(|l| l.contains("Set up the connection now?"))
        );
        assert_eq!(super::super::Steps::page(&w), Page::Setup);
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
