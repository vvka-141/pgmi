#![no_main]

use libfuzzer_sys::arbitrary::{Result, Unstructured};
use libfuzzer_sys::fuzz_target;
use pgmi::invariants::{self, Meta};

fn meta(u: &mut Unstructured<'_>) -> Result<Meta> {
    Ok(Meta {
        id: u.arbitrary()?,
        idempotent: u.arbitrary()?,
        sort_keys: u.arbitrary()?,
        description: u.arbitrary()?,
    })
}

fuzz_target!(|data: &[u8]| {
    let Some((&mode, rest)) = data.split_first() else {
        return;
    };
    let text = String::from_utf8_lossy(rest);
    let checked = match mode % 3 {
        0 => invariants::metadata_text(&text),
        1 => invariants::metadata_text(&format!(
            "/* <pgmi-meta id=\"1b5e3a4c-2d6f-4a8b-9c0d-1e2f3a4b5c6d\" idempotent=\"true\">{text}</pgmi-meta> */"
        )),
        _ => {
            let Ok(m) = meta(&mut Unstructured::new(rest)) else {
                return;
            };
            invariants::metadata_block(&m)
        }
    };
    if let Err(e) = checked {
        panic!("{e}");
    }
});
