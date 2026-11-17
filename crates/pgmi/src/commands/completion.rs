//! Shell completion as Go's binary does it: Cobra's scripts, which ask the
//! binary itself (`pgmi __complete <words>`) for the words to offer.

use std::fmt::Write as _;

use clap::{Arg, ArgAction, Command, CommandFactory};

use super::{stderr, stdout};
use crate::cli::{Cli, CompletionArgs, go_bool};
use crate::env::Env;
use crate::error::Result;
use crate::templates::Template;

pub(crate) const REQUEST: &str = "__complete";
pub(crate) const REQUEST_NO_DESCRIPTIONS: &str = "__completeNoDesc";

pub(crate) fn run(a: &CompletionArgs) -> Result<()> {
    stdout(match a.shell.as_str() {
        "bash" => include_str!("completion/pgmi.bash"),
        "zsh" => include_str!("completion/pgmi.zsh"),
        "fish" => include_str!("completion/pgmi.fish"),
        _ => include_str!("completion/pgmi.ps1"),
    })
}

/// Answers a script's `__complete` request: one word per line (a tab and
/// its description unless turned off), then `:<directive>`.
pub(crate) fn request(request: &str, words: &[String], env: &Env) -> Result<()> {
    let descriptions = request == REQUEST
        && [
            "PGMI_COMPLETION_DESCRIPTIONS",
            "COBRA_COMPLETION_DESCRIPTIONS",
        ]
        .into_iter()
        .find_map(|name| env.non_empty(name).ok().flatten())
        .is_none_or(|v| go_bool(v).unwrap_or(true));
    let answer = answer(words);
    let mut out = String::new();
    for (word, about) in &answer.words {
        let line = match about.lines().next() {
            Some(about) if descriptions => format!("{word}\t{about}"),
            _ => word.clone(),
        };
        let _ = writeln!(out, "{}", line.trim());
    }
    let _ = writeln!(out, ":{}", answer.directive.code());
    stdout(&out)?;
    stderr(&format!(
        "Completion ended with directive: {}\n",
        answer.directive.name()
    ));
    Ok(())
}

/// What the shell does besides offering the words.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Directive {
    Files,
    NoFiles,
    Directories,
}

impl Directive {
    fn code(self) -> u8 {
        match self {
            Directive::Files => 0,
            Directive::NoFiles => 4,
            Directive::Directories => 16,
        }
    }

    fn name(self) -> &'static str {
        match self {
            Directive::Files => "ShellCompDirectiveDefault",
            Directive::NoFiles => "ShellCompDirectiveNoFileComp",
            Directive::Directories => "ShellCompDirectiveFilterDirs",
        }
    }
}

#[derive(Debug, PartialEq, Eq)]
struct Answer {
    words: Vec<(String, String)>,
    directive: Directive,
}

impl Answer {
    fn empty(directive: Directive) -> Self {
        Answer {
            words: Vec::new(),
            directive,
        }
    }
}

/// What a command completes for its arguments, as Go's commands declare it.
enum Arguments {
    Choices,
    Templates,
    Directories,
    Commands,
    Nothing,
}

fn arguments(path: &[&str]) -> Arguments {
    match path {
        ["completion"] => Arguments::Choices,
        ["templates", "describe"] => Arguments::Templates,
        ["deploy" | "init"] | ["metadata", "plan" | "validate" | "scaffold"] => {
            Arguments::Directories
        }
        ["help"] => Arguments::Commands,
        _ => Arguments::Nothing,
    }
}

fn template_names() -> Vec<String> {
    let mut names: Vec<String> = Template::ALL.iter().map(|t| t.name().into()).collect();
    names.sort();
    names
}

fn answer(words: &[String]) -> Answer {
    let mut root = Cli::command();
    root.build();
    let (word, before) = match words.split_last() {
        Some((last, before)) => (last.as_str(), before),
        None => ("", words),
    };
    let before: Vec<&str> = before.iter().map(String::as_str).collect();
    let Some((path, cmd, rest)) = find(&root, &before) else {
        return Answer::empty(Directive::Files);
    };
    let top = path.is_empty();
    let dashdash = rest.contains(&"--");
    let (flag, rest, word) = match value_flag(cmd, &rest, word) {
        Ok(found) => found,
        Err(()) if dashdash => (None, rest, word),
        Err(()) => return Answer::empty(Directive::Files),
    };
    let Ok(parsed) = parse(cmd, &rest) else {
        return Answer::empty(Directive::Files);
    };
    if top && parsed.set.iter().any(|a| a.get_id() == "version") {
        return Answer::empty(Directive::NoFiles);
    }
    if let (Some(flag), false) = (flag, dashdash) {
        return flag_values(flag, word);
    }
    if word.starts_with('-') && !word.contains('=') && !dashdash {
        return Answer {
            words: flag_names(cmd, top, &parsed.set, word),
            directive: Directive::NoFiles,
        };
    }
    let mut answer = Answer::empty(Directive::Files);
    let local = parsed.set.iter().any(|a| !a.is_global_set());
    if parsed.args.is_empty() && !local {
        answer.words = commands(cmd, word);
        if cmd.has_subcommands() {
            answer.directive = Directive::NoFiles;
        }
    }
    match arguments(&path) {
        Arguments::Choices if parsed.args.is_empty() => {
            answer.words.extend(choices(first_positional(cmd), word));
            answer.directive = Directive::NoFiles;
        }
        Arguments::Templates | Arguments::Directories if !parsed.args.is_empty() => {
            answer.directive = Directive::NoFiles;
        }
        Arguments::Templates => {
            answer.words.extend(prefixed(template_names(), word));
            answer.directive = Directive::NoFiles;
        }
        Arguments::Directories => answer.directive = Directive::Directories,
        Arguments::Commands => {
            if let Some((_, named, _)) = find(&root, &parsed.args) {
                answer.words.extend(commands(named, word));
            }
            answer.directive = Directive::NoFiles;
        }
        Arguments::Choices | Arguments::Nothing => {}
    }
    answer
}

/// The command the words name and the words left for it; `None` for an
/// unknown command word.
fn find<'c, 'w>(
    root: &'c Command,
    words: &[&'w str],
) -> Option<(Vec<&'c str>, &'c Command, Vec<&'w str>)> {
    let mut cmd = root;
    let mut path = Vec::new();
    let mut rest = words.to_vec();
    while let Some(i) = first_word(cmd, &rest) {
        match cmd.find_subcommand(rest[i]) {
            Some(sub) => {
                rest.remove(i);
                path.push(sub.get_name());
                cmd = sub;
            }
            None if path.is_empty() && cmd.has_subcommands() => return None,
            None => break,
        }
    }
    Some((path, cmd, rest))
}

fn first_word(cmd: &Command, words: &[&str]) -> Option<usize> {
    let mut i = 0;
    while i < words.len() {
        let w = words[i];
        if w == "--" {
            return None;
        }
        let takes_next = !w.contains('=')
            && match w.strip_prefix("--") {
                Some(long) => !lookup_long(cmd, long).is_some_and(no_value),
                None => {
                    w.len() == 2
                        && w.starts_with('-')
                        && !w[1..]
                            .chars()
                            .next()
                            .and_then(|c| lookup_short(cmd, c))
                            .is_some_and(no_value)
                }
            };
        if takes_next {
            i += 2;
            continue;
        }
        if !w.is_empty() && !w.starts_with('-') {
            return Some(i);
        }
        i += 1;
    }
    None
}

fn lookup_long<'c>(cmd: &'c Command, name: &str) -> Option<&'c Arg> {
    cmd.get_arguments()
        .find(|a| !a.is_positional() && !a.is_hide_set() && a.get_long() == Some(name))
}

fn lookup_short(cmd: &Command, c: char) -> Option<&Arg> {
    cmd.get_arguments()
        .find(|a| !a.is_positional() && !a.is_hide_set() && a.get_short() == Some(c))
}

fn no_value(arg: &Arg) -> bool {
    !arg.get_action().takes_values() || arg.get_num_args().is_some_and(|n| n.min_values() == 0)
}

fn is_flag(word: &str) -> bool {
    (word.len() >= 3 && word.starts_with("--"))
        || (word.len() >= 2 && word.starts_with('-') && !word.starts_with("--"))
}

/// The flag whose value is being typed, either glued (`--sslmode=v`) or the
/// word before (`--sslmode v`); `Err` for a flag the command does not have.
type ValueFlag<'c, 'w> = (Option<&'c Arg>, Vec<&'w str>, &'w str);

fn value_flag<'c, 'w>(
    cmd: &'c Command,
    rest: &[&'w str],
    word: &'w str,
) -> std::result::Result<ValueFlag<'c, 'w>, ()> {
    let lookup = |name: &str| match name.strip_prefix("--") {
        Some(long) => lookup_long(cmd, long),
        None => name.chars().last().and_then(|c| lookup_short(cmd, c)),
    };
    if word.starts_with('-') {
        let Some((name, value)) = word.split_once('=') else {
            return Ok((None, rest.to_vec(), word));
        };
        return lookup(name)
            .map(|f| (Some(f), rest.to_vec(), value))
            .ok_or(());
    }
    match rest.split_last() {
        Some((prev, before)) if is_flag(prev) && !prev.contains('=') => match lookup(prev) {
            None => Err(()),
            Some(f) if no_value(f) => Ok((None, rest.to_vec(), word)),
            Some(f) => Ok((Some(f), before.to_vec(), word)),
        },
        _ => Ok((None, rest.to_vec(), word)),
    }
}

struct Parsed<'c, 'w> {
    args: Vec<&'w str>,
    set: Vec<&'c Arg>,
}

fn parse<'c, 'w>(cmd: &'c Command, words: &[&'w str]) -> std::result::Result<Parsed<'c, 'w>, ()> {
    let mut parsed = Parsed {
        args: Vec::new(),
        set: Vec::new(),
    };
    let mut words = words.iter();
    while let Some(&w) = words.next() {
        if w == "--" {
            parsed.args.extend(words.by_ref());
            break;
        }
        if let Some(body) = w.strip_prefix("--") {
            let (name, value) = match body.split_once('=') {
                Some((n, v)) => (n, Some(v)),
                None => (body, None),
            };
            let arg = lookup_long(cmd, name).ok_or(())?;
            if value.is_none() && !no_value(arg) {
                words.next().ok_or(())?;
            }
            parsed.set.push(arg);
        } else if w.len() > 1 && w.starts_with('-') {
            let shorts = &w[1..];
            for (i, c) in shorts.char_indices() {
                let arg = lookup_short(cmd, c).ok_or(())?;
                parsed.set.push(arg);
                let after = &shorts[i + c.len_utf8()..];
                if after.starts_with('=') {
                    break;
                }
                if no_value(arg) {
                    continue;
                }
                if after.is_empty() {
                    words.next().ok_or(())?;
                }
                break;
            }
        } else {
            parsed.args.push(w);
        }
    }
    Ok(parsed)
}

fn prefixed(values: Vec<String>, word: &str) -> impl Iterator<Item = (String, String)> {
    values
        .into_iter()
        .filter(move |v| v.starts_with(word))
        .map(|v| (v, String::new()))
}

fn possible_values(arg: &Arg) -> Vec<String> {
    if arg.get_id() == "template" {
        return template_names();
    }
    if arg.get_id() == "sslmode" {
        return crate::conn::resolve::SslMode::ALL
            .iter()
            .map(|m| m.as_str().to_string())
            .collect();
    }
    arg.get_possible_values()
        .iter()
        .filter(|v| !v.is_hide_set())
        .map(|v| v.get_name().to_string())
        .collect()
}

fn first_positional(cmd: &Command) -> Option<&Arg> {
    cmd.get_arguments()
        .find(|a| a.is_positional() && a.get_index() == Some(1))
}

fn choices(arg: Option<&Arg>, word: &str) -> Vec<(String, String)> {
    arg.map(|a| prefixed(possible_values(a), word).collect())
        .unwrap_or_default()
}

fn flag_values(flag: &Arg, word: &str) -> Answer {
    let values = possible_values(flag);
    if values.is_empty() {
        return Answer::empty(Directive::Files);
    }
    Answer {
        words: prefixed(values, word).collect(),
        directive: Directive::NoFiles,
    }
}

fn about(text: Option<&clap::builder::StyledStr>) -> String {
    text.map(ToString::to_string).unwrap_or_default()
}

fn flag_names(cmd: &Command, top: bool, set: &[&Arg], word: &str) -> Vec<(String, String)> {
    let mut flags: Vec<&Arg> = cmd
        .get_arguments()
        .filter(|a| !a.is_positional() && !a.is_hide_set() && a.get_long().is_some())
        .filter(|a| {
            matches!(a.get_action(), ArgAction::Append)
                || !set.iter().any(|s| s.get_id() == a.get_id())
        })
        .collect();
    flags.sort_by_key(|a| (top || !a.is_global_set(), a.get_long()));
    let mut words = Vec::new();
    for a in flags {
        let help = about(a.get_help());
        let long = format!("--{}", a.get_long().unwrap_or_default());
        if long.starts_with(word) {
            words.push((long, help.clone()));
        }
        if let Some(short) = a.get_short().map(|s| format!("-{s}"))
            && short.starts_with(word)
        {
            words.push((short, help));
        }
    }
    words
}

fn commands(cmd: &Command, word: &str) -> Vec<(String, String)> {
    let mut subs: Vec<&Command> = cmd.get_subcommands().filter(|s| !s.is_hide_set()).collect();
    subs.sort_by_key(|s| s.get_name());
    subs.into_iter()
        .filter(|s| s.get_name().starts_with(word))
        .map(|s| (s.get_name().to_string(), about(s.get_about())))
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn complete(words: &[&str]) -> (String, u8) {
        let words: Vec<String> = words.iter().map(|w| (*w).to_string()).collect();
        let a = answer(&words);
        let list: Vec<String> = a.words.into_iter().map(|(w, _)| w).collect();
        (list.join(" "), a.directive.code())
    }

    #[test]
    fn answers_as_go_does() {
        for (words, offered, directive) in [
            (
                &[""][..],
                "ai completion config deploy help info init metadata serve templates version",
                4,
            ),
            (&["--"], "--help --verbose --version", 4),
            (
                &["-v", ""],
                "ai completion config deploy help info init metadata serve templates version",
                4,
            ),
            (&["nosuch", ""], "", 0),
            (&["--version", ""], "", 4),
            (
                &["deploy", "--ss"],
                "--sslcert --sslkey --sslmode --sslrootcert",
                4,
            ),
            (
                &["deploy", "-v", "--s"],
                "--sslcert --sslkey --sslmode --sslrootcert",
                4,
            ),
            (
                &["deploy", "--sslmode", "x", "--s"],
                "--sslcert --sslkey --sslrootcert",
                4,
            ),
            (
                &["deploy", "--param", "a=1", "--pa"],
                "--param --params-file",
                4,
            ),
            (&["deploy", "-h", "x", "--us"], "--username", 4),
            (&["deploy", "--verbose=true", "--he"], "--help", 4),
            (
                &["deploy", "-d", "x", "--sslmode", ""],
                "disable allow prefer require verify-ca verify-full",
                4,
            ),
            (&["deploy", "--sslmode=v"], "verify-ca verify-full", 4),
            (&["deploy", "--timeout", ""], "", 0),
            (&["deploy", "--nosuch", ""], "", 0),
            (&["deploy", ""], "", 16),
            (&["deploy", "--overwrite", ""], "", 16),
            (&["deploy", "--help", ""], "", 16),
            (&["deploy", "--", ""], "", 16),
            (&["deploy", "--", "--s"], "", 16),
            (&["deploy", "x", ""], "", 4),
            (&["init", "--template", ""], "advanced basic minimal", 4),
            (&["init", "-t", ""], "advanced basic minimal", 4),
            (&["init", "--template="], "advanced basic minimal", 4),
            (&["init", "-t=b"], "basic", 4),
            (&["init", "-t"], "-t", 4),
            (&["init", "-t", "basic", ""], "", 16),
            (&["metadata", ""], "plan scaffold validate", 4),
            (&["metadata", "plan", ""], "", 16),
            (&["metadata", "scaffold", "--sort-key", "x", ""], "", 16),
            (&["templates", ""], "describe list", 4),
            (&["templates", "--"], "--help --verbose", 4),
            (&["templates", "describe", ""], "advanced basic minimal", 4),
            (&["templates", "describe", "basic", ""], "", 4),
            (&["templates", "nosuch", ""], "", 0),
            (&["ai", ""], "contract", 4),
            (&["completion", ""], "bash zsh fish powershell", 4),
            (&["completion", "bash", ""], "", 0),
            (
                &["help", ""],
                "ai completion config deploy help info init metadata serve templates version",
                4,
            ),
            (&["help", "templates", ""], "describe list", 4),
            (&["help", "nosuch", ""], "", 4),
            (&["help", "deploy", "x", ""], "", 4),
            (&["info", ""], "", 0),
            (&["version", ""], "", 0),
        ] {
            assert_eq!(
                complete(words),
                (offered.to_string(), directive),
                "{words:?}"
            );
        }
    }

    #[test]
    fn deploy_lists_inherited_flags_first_then_its_own_by_name() {
        let (offered, _) = complete(&["deploy", "-"]);
        assert!(
            offered.starts_with("--help --verbose -v --aws --aws-region --azure "),
            "{offered}"
        );
        assert!(
            offered.contains(" --database -d ") && offered.ends_with(" --username -U"),
            "{offered}"
        );
    }

    #[test]
    fn every_script_asks_the_binary() {
        for shell in ["bash", "zsh", "fish", "powershell"] {
            let text = match shell {
                "bash" => include_str!("completion/pgmi.bash"),
                "zsh" => include_str!("completion/pgmi.zsh"),
                "fish" => include_str!("completion/pgmi.fish"),
                _ => include_str!("completion/pgmi.ps1"),
            };
            assert!(text.contains(REQUEST), "{shell}");
        }
    }
}
