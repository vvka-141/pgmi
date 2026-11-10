import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CONTRACT = ROOT / "docs" / "design" / "behaviour-contract.md"
CASES = ROOT / "blackbox" / "cases"

CASE_REF = re.compile(r"`([a-z0-9-]+/[A-Za-z0-9_.-]+)`")


def sections() -> dict[str, str]:
    text = CONTRACT.read_text()
    parts = re.split(r"^### (.+)$", text, flags=re.MULTILINE)
    return dict(zip(parts[1::2], parts[2::2], strict=True))


def cited() -> set[str]:
    return {m for body in sections().values() for m in CASE_REF.findall(body) if (CASES / f"{m}.yaml").exists()}


def test_every_case_is_cited():
    all_cases = {str(p.relative_to(CASES).with_suffix("")) for p in CASES.rglob("*.yaml")}
    missing = sorted(all_cases - cited())
    assert not missing, f"cases the contract does not cite: {missing}"


def test_every_cited_case_exists():
    refs = {
        m
        for body in sections().values()
        for line in body.splitlines()
        if line.startswith("Cases:")
        for m in CASE_REF.findall(line)
    }
    unknown = sorted(r for r in refs if not (CASES / f"{r}.yaml").exists())
    assert not unknown, f"cited cases that do not exist: {unknown}"


def test_every_section_names_cases_or_a_reason():
    bare = [title for title, body in sections().items() if "Cases:" not in body and "No case" not in body]
    assert not bare, f"sections with neither cases nor a reason: {bare}"


def test_no_go_identifiers():
    hits = re.findall(r"internal/|pkg/|\bfunc |\.go\b", CONTRACT.read_text())
    assert not hits, f"Go identifiers in the contract: {hits}"
