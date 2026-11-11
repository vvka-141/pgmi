#![no_main]

use libfuzzer_sys::fuzz_target;
use pgmi::conn::tap;

const POLICIES: [Option<&str>; 4] = [None, Some("scram-sha-256"), Some("!md5"), Some("password,md5")];

fuzz_target!(|data: &[u8]| {
    let Some((&head, rest)) = data.split_first() else {
        return;
    };
    let policy = POLICIES[usize::from(head) % POLICIES.len()];
    let Ok(shared) = tap::Shared::new(policy) else {
        return;
    };
    let chunk = usize::from(head >> 2).max(1);
    let mut parser = tap::Parser::default();
    for part in rest.chunks(chunk) {
        if parser.feed(part, &shared).is_err() {
            return;
        }
    }
    let _ = shared.status();
});
