#!/usr/bin/env python3
"""Translate // comment lines containing accents via Google Translate."""
import json
import re
import sys
import urllib.parse
import urllib.request
from pathlib import Path

ACCENT = re.compile(r"[àâäéèêëïîôùûüçÀÂÄÉÈÊËÏÎÔÙÛÜÇ]")


def translate(text: str) -> str:
    q = urllib.parse.quote(text)
    url = (
        "https://translate.googleapis.com/translate_a/single"
        f"?client=gtx&sl=fr&tl=en&dt=t&q={q}"
    )
    req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0"})
    with urllib.request.urlopen(req, timeout=60) as resp:
        data = json.loads(resp.read().decode())
    return "".join(part[0] for part in data[0] if part[0])


def process_line(line: str) -> str:
    if not ACCENT.search(line):
        return line
    m = re.match(r"^(\s*)//(.*)$", line)
    if m:
        indent, body = m.group(1), m.group(2)
        en = translate(body.strip())
        if not en.startswith(" "):
            en = " " + en
        return indent + "//" + en
    if "//" in line:
        code, _, comment = line.partition("//")
        if ACCENT.search(comment):
            en = translate(comment.strip())
            return code + "// " + en
    return line


def process_file(path: Path) -> None:
    lines = path.read_text(encoding="utf-8").splitlines(keepends=True)
    out = []
    for line in lines:
        out.append(process_line(line.rstrip("\n")) + "\n")
    path.write_text("".join(out), encoding="utf-8")


def main() -> int:
    for arg in sys.argv[1:]:
        process_file(Path(arg))
        n = len(ACCENT.findall(Path(arg).read_text(encoding="utf-8")))
        print(f"{n:4d}  {arg}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
