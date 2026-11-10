#![no_main]
#![allow(dead_code)]

#[path = "../../src/serve/wire.rs"]
mod wire;

use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    let Some((&head, rest)) = data.split_first() else {
        return;
    };
    let chunk = usize::from(head).max(1);
    let mut lines = wire::Lines::default();
    let mut all = Vec::new();
    for part in rest.chunks(chunk) {
        all.extend(lines.feed(part));
    }
    all.extend(lines.finish());
    let expected = rest.split(|b| *b == b'\n').filter(|s| !s.is_empty()).count();
    assert_eq!(all.iter().filter(|l| !l.is_empty()).count(), expected);
    for line in all {
        assert!(!line.contains(&b'\n'));
        match wire::decode(&line) {
            wire::Decoded::Reply(r) => {
                assert!(!r.contains('\n'));
                assert!(serde_json::from_str::<serde_json::Value>(&r).is_ok(), "{r}");
            }
            wire::Decoded::Request(r) | wire::Decoded::Notification(r) => {
                if let Some(id) = r.id {
                    assert!(serde_json::from_str::<serde_json::Value>(&id).is_ok(), "{id}");
                }
                if let Some(p) = r.params {
                    assert!(serde_json::from_str::<&serde_json::value::RawValue>(p.get()).is_ok());
                }
            }
            wire::Decoded::Skip => {}
        }
    }
});
