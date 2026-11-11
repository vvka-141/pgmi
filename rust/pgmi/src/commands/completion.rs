use std::fmt::Write as _;

use clap::{Command, CommandFactory};
use clap_complete::Shell;

use super::stdout;
use crate::cli::{Cli, CompletionArgs};
use crate::error::{Error, Result};

const POWERSHELL_SWITCH: &str = "    $completions = @(switch ($command) {";

pub(crate) fn run(a: &CompletionArgs) -> Result<()> {
    stdout(&script(&a.shell)?)
}

fn script(shell: &str) -> Result<String> {
    let shell = match shell {
        "bash" => Shell::Bash,
        "zsh" => Shell::Zsh,
        "fish" => Shell::Fish,
        _ => Shell::PowerShell,
    };
    let mut cli = Cli::command();
    let mut buf = Vec::new();
    clap_complete::generate(shell, &mut cli, "pgmi", &mut buf);
    let text =
        String::from_utf8(buf).map_err(|e| Error::internal("completion script").with_source(e))?;
    if shell == Shell::Fish {
        let mut lines = String::new();
        fish_positionals(&cli, &[], &mut lines);
        return Ok(text + &lines);
    }
    if shell != Shell::PowerShell {
        return Ok(text);
    }
    if !text.contains(POWERSHELL_SWITCH) {
        return Err(Error::internal(
            "completion script: the PowerShell generator changed shape",
        ));
    }
    Ok(text.replacen(
        POWERSHELL_SWITCH,
        &format!("{}{POWERSHELL_SWITCH}", powershell_values(&cli)),
        1,
    ))
}

fn first_positional_values(cmd: &Command) -> Vec<String> {
    cmd.get_arguments()
        .find(|a| a.is_positional() && a.get_index() == Some(1) && !a.is_hide_set())
        .map(|a| {
            a.get_possible_values()
                .iter()
                .filter(|v| !v.is_hide_set())
                .map(|v| v.get_name().to_string())
                .collect()
        })
        .unwrap_or_default()
}

/// `clap_complete`'s fish script completes no positional values; these lines
/// offer a subcommand's fixed first argument, as Go does.
fn fish_positionals(cmd: &Command, path: &[&str], out: &mut String) {
    let values = first_positional_values(cmd);
    if let ([first, rest @ ..], false) = (path, values.is_empty()) {
        let mut condition = format!("__fish_pgmi_using_subcommand {first}");
        for sub in rest {
            let _ = write!(condition, "; and __fish_seen_subcommand_from {sub}");
        }
        let _ = writeln!(
            out,
            "complete -c pgmi -n \"{condition}\" -f -a \"{}\"",
            values.join(" ")
        );
    }
    for sub in cmd.get_subcommands() {
        let mut deeper = path.to_vec();
        deeper.push(sub.get_name());
        fish_positionals(sub, &deeper, out);
    }
}

/// `clap_complete`'s PowerShell script completes names only; this block
/// completes the values of options that take one of a fixed set, as Go does.
fn powershell_values(cli: &Command) -> String {
    let mut cases = String::new();
    value_cases(cli, "pgmi", &mut cases);
    format!(
        "    $previous = if ($wordToComplete -eq '') {{ \"$($commandElements[-1])\" }} else {{ \"$($commandElements[-2])\" }}\n\
         \x20   $values = switch -Wildcard (\"$command|$previous\") {{\n{cases}\x20   }}\n\
         \x20   if ($values) {{\n\
         \x20       $values | Where-Object {{ $_ -like \"$wordToComplete*\" }} | ForEach-Object {{\n\
         \x20           [CompletionResult]::new($_, $_, [CompletionResultType]::ParameterValue, $_)\n\
         \x20       }}\n\
         \x20       return\n\
         \x20   }}\n\n"
    )
}

fn value_cases(cmd: &Command, path: &str, out: &mut String) {
    for arg in cmd.get_arguments() {
        if arg.is_hide_set() || !arg.get_action().takes_values() {
            continue;
        }
        let values: Vec<String> = arg
            .get_possible_values()
            .iter()
            .filter(|v| !v.is_hide_set())
            .map(|v| format!("'{}'", v.get_name()))
            .collect();
        if values.is_empty() {
            continue;
        }
        if arg.is_positional() {
            if arg.get_index() == Some(1) {
                let _ = writeln!(
                    out,
                    "        '{path}|{}' {{ {} }}",
                    cmd.get_name(),
                    values.join(", ")
                );
            }
            continue;
        }
        let names = arg
            .get_short()
            .map(|s| format!("-{s}"))
            .into_iter()
            .chain(arg.get_long().map(|l| format!("--{l}")));
        for name in names {
            let _ = writeln!(out, "        '{path}*|{name}' {{ {} }}", values.join(", "));
        }
    }
    for sub in cmd.get_subcommands() {
        value_cases(sub, &format!("{path};{}", sub.get_name()), out);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn powershell_completes_template_names_and_sslmode_values() {
        let text = script("powershell").unwrap();
        let block = text.split(POWERSHELL_SWITCH).next().unwrap();
        for case in [
            "'pgmi;deploy*|--sslmode' { 'disable', 'allow', 'prefer', 'require', 'verify-ca', 'verify-full' }",
            "'pgmi;init*|-t' { 'minimal', 'basic', 'advanced' }",
            "'pgmi;init*|--template' { 'minimal', 'basic', 'advanced' }",
            "'pgmi;templates;describe|describe' { 'minimal', 'basic', 'advanced' }",
        ] {
            assert!(block.contains(case), "missing {case}");
        }
        assert!(
            !block.contains("*|--json'"),
            "a flag without fixed values got values"
        );
    }

    #[test]
    fn bash_zsh_and_fish_complete_template_names_after_describe() {
        assert!(script("fish").unwrap().contains(
            "complete -c pgmi -n \"__fish_pgmi_using_subcommand templates; and __fish_seen_subcommand_from describe\" -f -a \"minimal basic advanced\""
        ));
        assert!(
            script("zsh")
                .unwrap()
                .contains("':name:(minimal basic advanced)'")
        );
        let bash = script("bash").unwrap();
        let describe = bash
            .split("pgmi__subcmd__templates__subcmd__describe)")
            .nth(1)
            .and_then(|s| s.split(";;").next())
            .unwrap();
        assert!(describe.contains("minimal basic advanced"), "{describe}");
    }

    #[test]
    fn other_shells_are_clap_complete_unchanged() {
        assert!(!script("fish").unwrap().contains("$previous"));
    }
}
