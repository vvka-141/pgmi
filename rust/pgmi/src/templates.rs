//! The project templates `pgmi init` scaffolds: names, summaries and the
//! long descriptions `pgmi templates describe` prints.

pub(crate) const NAMES: [&str; 3] = ["minimal", "basic", "advanced"];

pub(crate) const LIST: [(&str, &str, &str); 3] = [
    (
        "minimal",
        "A deploy.sql loop, hello_world.sql and a .pgmiignore; nothing else",
        "Building your own deployment from nothing",
    ),
    (
        "basic",
        "Two layers: run-once tables, re-run views and functions",
        "Start here. Deploying SQL — small, explicit, any managed provider",
    ),
    (
        "advanced",
        "Reference app: SQL-native REST/RPC/MCP, multi-tenant RLS, audit",
        "SQL-native application stack — scaffold, read, trim to fit",
    ),
];

pub(crate) fn describe(name: &str) -> Option<&'static str> {
    match name {
        "minimal" => Some(include_str!("templates/minimal.txt")),
        "basic" => Some(include_str!("templates/basic.txt")),
        "advanced" => Some(include_str!("templates/advanced.txt")),
        _ => None,
    }
}
