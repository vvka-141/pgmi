#![no_main]

use libfuzzer_sys::fuzz_target;
use pgmi::invariants;

fuzz_target!(|data: &[u8]| {
    let Some((&mode, rest)) = data.split_first() else {
        return;
    };
    let checked = if mode % 2 == 0 {
        invariants::sql_bytes(rest)
    } else {
        invariants::sql_fragments(rest)
    };
    if let Err(e) = checked {
        panic!("{e}");
    }
});
