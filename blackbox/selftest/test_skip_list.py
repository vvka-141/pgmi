from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SKIP = ROOT / "rust" / "blackbox-skip.txt"
CASES = ROOT / "blackbox" / "cases"


def test_every_skipped_case_exists_and_says_why():
    for line in SKIP.read_text().splitlines():
        if not line.strip() or line.startswith("#"):
            continue
        case, _, why = line.partition("#")
        assert (CASES / f"{case.strip()}.yaml").exists(), f"no such case: {case.strip()}"
        assert why.strip(), f"no reason given: {line}"
