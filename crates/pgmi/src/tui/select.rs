use crossterm::event::{KeyCode, KeyEvent, KeyEventKind, KeyModifiers};
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Outcome<T> {
    Picked(T),
    Back,
    Cancel,
}

/// One choice: the value it stands for, its label and the line under it.
#[derive(Debug, Clone)]
pub(crate) struct Choice<T> {
    pub(crate) value: T,
    pub(crate) label: String,
    pub(crate) detail: String,
}

impl<T> Choice<T> {
    pub(crate) fn new(value: T, label: impl Into<String>, detail: impl Into<String>) -> Self {
        Choice {
            value,
            label: label.into(),
            detail: detail.into(),
        }
    }
}

/// A list of choices and a cursor on one of them. Every list is built from a
/// constant, non-empty catalogue.
#[derive(Debug, Clone)]
pub(crate) struct Select<T> {
    pub(crate) prompt: String,
    choices: Vec<Choice<T>>,
    pub(crate) help: &'static str,
    pub(crate) cursor: usize,
}

pub(crate) const ACCENT: Color = Color::Indexed(39);
pub(crate) const MUTED: Color = Color::Indexed(245);
pub(crate) const FAINT: Color = Color::Indexed(240);

impl<T: Copy> Select<T> {
    pub(crate) fn new(
        prompt: impl Into<String>,
        choices: Vec<Choice<T>>,
        help: &'static str,
    ) -> Self {
        Select {
            prompt: prompt.into(),
            choices,
            help,
            cursor: 0,
        }
    }

    /// The value under the cursor; `None` only for an empty list.
    pub(crate) fn selected(&self) -> Option<T> {
        self.choices.get(self.cursor).map(|c| c.value)
    }

    pub(crate) fn update(&mut self, key: KeyEvent) -> Option<Outcome<T>> {
        if key.kind == KeyEventKind::Release {
            return None;
        }
        let last = self.choices.len().saturating_sub(1);
        let control = key.modifiers.contains(KeyModifiers::CONTROL);
        match key.code {
            KeyCode::Char('c') if control => Some(Outcome::Cancel),
            KeyCode::Char(_) if control => None,
            KeyCode::Up | KeyCode::Char('k') => {
                self.cursor = if self.cursor == 0 {
                    last
                } else {
                    self.cursor - 1
                };
                None
            }
            KeyCode::Down | KeyCode::Char('j') | KeyCode::Tab => {
                self.cursor = if self.cursor >= last {
                    0
                } else {
                    self.cursor + 1
                };
                None
            }
            KeyCode::Enter => self.selected().map(Outcome::Picked),
            KeyCode::Esc => Some(Outcome::Back),
            _ => None,
        }
    }

    pub(crate) fn view(&self, title: &str) -> Vec<Line<'static>> {
        let mut lines = vec![
            Line::from(Span::styled(
                title.to_string(),
                Style::new().fg(ACCENT).add_modifier(Modifier::BOLD),
            )),
            Line::default(),
            Line::from(Span::styled(self.prompt.clone(), Style::new().fg(MUTED))),
            Line::default(),
        ];
        for (i, Choice { label, detail, .. }) in self.choices.iter().enumerate() {
            let (mark, style) = if i == self.cursor {
                ("●", Style::new().fg(ACCENT).add_modifier(Modifier::BOLD))
            } else {
                ("○", Style::new().fg(MUTED))
            };
            lines.push(Line::from(vec![
                Span::raw("  "),
                Span::styled(format!("{mark} {label}"), style),
            ]));
            lines.push(Line::from(Span::styled(
                format!("    {detail}"),
                Style::new().fg(FAINT),
            )));
        }
        lines.push(Line::default());
        lines.push(Line::from(Span::styled(self.help, Style::new().fg(FAINT))));
        lines
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn key(code: KeyCode) -> KeyEvent {
        KeyEvent::new(code, KeyModifiers::NONE)
    }

    #[test]
    fn moves_and_picks() {
        let mut s = Select::new(
            "p",
            vec![Choice::new('a', "a", ""), Choice::new('b', "b", "")],
            "",
        );
        assert_eq!(s.update(key(KeyCode::Down)), None);
        assert_eq!(s.update(key(KeyCode::Enter)), Some(Outcome::Picked('b')));
        assert_eq!(s.update(key(KeyCode::Down)), None);
        assert_eq!(s.cursor, 0);
        assert_eq!(s.update(key(KeyCode::Up)), None);
        assert_eq!(s.cursor, 1);
        assert_eq!(s.update(key(KeyCode::Esc)), Some(Outcome::Back));
        assert_eq!(
            s.update(KeyEvent::new(KeyCode::Char('j'), KeyModifiers::CONTROL)),
            None
        );
        assert_eq!(s.cursor, 1);
        assert_eq!(
            s.update(KeyEvent::new(KeyCode::Char('c'), KeyModifiers::CONTROL)),
            Some(Outcome::Cancel)
        );
    }

    #[test]
    fn enter_on_an_empty_list_picks_nothing() {
        let mut s = Select::<char>::new("p", Vec::new(), "");
        assert_eq!(s.update(key(KeyCode::Enter)), None);
    }
}
