#![no_main]

use libfuzzer_sys::arbitrary::{Result, Unstructured};
use libfuzzer_sys::fuzz_target;
use pgmi::invariants::{self, Conn};

fn conn(u: &mut Unstructured<'_>) -> Result<Conn> {
    Ok(Conn {
        hosts: u.arbitrary()?,
        dbname: u.arbitrary()?,
        user: u.arbitrary()?,
        password: u.arbitrary()?,
        sslmode: u.arbitrary()?,
        application_name: u.arbitrary()?,
    })
}

fuzz_target!(|data: &[u8]| {
    let Some((&mode, rest)) = data.split_first() else {
        return;
    };
    let checked = if mode % 2 == 0 {
        invariants::conn_text(&String::from_utf8_lossy(rest))
    } else {
        let Ok(c) = conn(&mut Unstructured::new(rest)) else {
            return;
        };
        invariants::conn_forms(&c)
    };
    if let Err(e) = checked {
        panic!("{e}");
    }
});
