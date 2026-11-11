#![no_main]

use libfuzzer_sys::arbitrary::Unstructured;
use libfuzzer_sys::fuzz_target;
use pgmi::invariants;

fuzz_target!(|data: &[u8]| {
    let Some((&mode, rest)) = data.split_first() else {
        return;
    };
    let mut u = Unstructured::new(rest);
    let checked = if mode % 2 == 0 {
        let Ok(([host, port, db, user], text)) = u.arbitrary::<([String; 4], String)>() else {
            return;
        };
        invariants::pgpass_text(&text, &host, &port, &db, &user)
    } else {
        let Ok(fields) = u.arbitrary::<[String; 5]>() else {
            return;
        };
        invariants::pgpass_entry(fields.each_ref().map(String::as_str))
    };
    if let Err(e) = checked {
        panic!("{e}");
    }
});
