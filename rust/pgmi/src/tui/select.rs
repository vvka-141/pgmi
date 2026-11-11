use crossterm::event::{KeyCode, KeyEvent, KeyEventKind, KeyModifiers};
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Outcome {
    Picked(usize),
    Back,
    Cancel,
}

#[derive(Debug, Clone)]
pub(crate) struct Select {
    pub(crate) prompt: String,
    pub(crate) choices: Vec<(String, String)>,
    pub(crate) help: &'static str,
    pub(crate) cursor: usize,
}

pub(crate) const ACCENT: Color = Color::Indexed(39);
pub(crate) const MUTED: Color = Color::Indexed(245);
pub(crate) const FAINT: Color = Color::Indexed(240);

impl Select {
    pub(crate) fn update(&mut self, key: KeyEvent) -> Option<Outcome> {
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
            KeyCode::Enter => Some(Outcome::Picked(self.cursor)),
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
        for (i, (label, detail)) in self.choices.iter().enumerate() {
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
        let mut s = Select {
            prompt: "p".into(),
            choices: vec![("a".into(), "".into()), ("b".into(), "".into())],
            help: "",
            cursor: 0,
        };
        assert_eq!(s.update(key(KeyCode::Down)), None);
        assert_eq!(s.update(key(KeyCode::Enter)), Some(Outcome::Picked(1)));
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
}
