#![no_main]

use libfuzzer_sys::arbitrary::Unstructured;
use libfuzzer_sys::fuzz_target;
use pgmi::invariants;

fuzz_target!(|data: &[u8]| {
    let Some((&mode, rest)) = data.split_first() else {
        return;
    };
    let checked = if mode % 2 == 0 {
        invariants::dotenv_text(&String::from_utf8_lossy(rest))
    } else {
        let Ok(pairs) = Unstructured::new(rest).arbitrary::<Vec<(String, String)>>() else {
            return;
        };
        invariants::dotenv_pairs(&pairs)
    };
    if let Err(e) = checked {
        panic!("{e}");
    }
});
