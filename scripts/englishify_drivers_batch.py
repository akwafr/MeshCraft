#!/usr/bin/env python3
"""Translate French // comments and quoted strings containing accented chars."""
from __future__ import annotations

import json
import os
import re
import sys
import time
import urllib.parse
import urllib.request

ACCENT = re.compile(r"[àâäéèêëïîôùûüçÀÂÄÉÈÊËÏÎÔÙÛÜÇ]")
CACHE: dict[str, str] = {}


def gtranslate(text: str) -> str:
    if text in CACHE:
        return CACHE[text]
    if len(text) > 3500:
        parts = re.split(r"(?<=[.;:!?])\s+", text)
        if len(parts) == 1:
            mid = len(text) // 2
            parts = [text[:mid], text[mid:]]
        return " ".join(gtranslate(p) for p in parts if p.strip())
    q = urllib.parse.urlencode(
        {"client": "gtx", "sl": "fr", "tl": "en", "dt": "t", "q": text}
    )
    url = "https://translate.googleapis.com/translate_a/single?" + q
    req = urllib.request.Request(url, headers={"User-Agent": "meshcraft-englishify/1.0"})
    last_err = None
    for attempt in range(5):
        try:
            with urllib.request.urlopen(req, timeout=60) as resp:
                data = json.loads(resp.read().decode())
            out = "".join(p[0] for p in data[0] if p[0])
            CACHE[text] = out
            time.sleep(0.12)
            return out
        except Exception as err:
            last_err = err
            time.sleep(1.5 * (attempt + 1))
    raise last_err


def translate_comment_body(body: str) -> str:
    body = body.rstrip()
    if not ACCENT.search(body):
        return body
    # Preserve leading space after //
    lead = len(body) - len(body.lstrip(" "))
    prefix = body[:lead]
    rest = body[lead:]
    if not ACCENT.search(rest):
        return body
    return prefix + gtranslate(rest)


def translate_quoted(s: str) -> str:
    """s includes surrounding quotes."""
    inner = s[1:-1]
    if not ACCENT.search(inner):
        return s
    # Do not translate format verbs or %q names
    return '"' + gtranslate(inner) + '"'


def process_line(line: str) -> str:
    if not ACCENT.search(line):
        return line

    m = re.match(r"^(\s*)//(.*)$", line)
    if m:
        return m.group(1) + "//" + translate_comment_body(m.group(2))

    if "//" in line:
        i = line.index("//")
        code, com = line[:i], line[i + 2 :]
        if ACCENT.search(com):
            return code + "//" + translate_comment_body(com)

    # Replace each double-quoted segment containing accents
    out = []
    pos = 0
    for m in re.finditer(r'"(?:\\.|[^"\\])*"', line):
        out.append(line[pos : m.start()])
        seg = m.group(0)
        out.append(translate_quoted(seg) if ACCENT.search(seg) else seg)
        pos = m.end()
    out.append(line[pos:])
    return "".join(out)


def process_file(path: str) -> int:
    with open(path, encoding="utf-8") as f:
        lines = f.readlines()
    changed = 0
    new_lines = []
    for i, raw in enumerate(lines):
        line = raw.rstrip("\n")
        nl = process_line(line)
        if nl != line:
            changed += 1
            if changed % 25 == 0:
                sys.stderr.write(f"{path}: {changed}...\n")
        new_lines.append(nl + "\n")
    with open(path, "w", encoding="utf-8") as f:
        f.writelines(new_lines)
    return changed


def main() -> None:
    paths: list[str] = []
    for arg in sys.argv[1:]:
        if arg.endswith(".go"):
            paths.append(arg)
            continue
        for root, _, files in os.walk(arg):
            for f in files:
                if f.endswith(".go"):
                    paths.append(os.path.join(root, f))
    total = 0
    for path in sorted(set(paths)):
        n = process_file(path)
        print(f"{path}: {n} lines changed (cache {len(CACHE)})")
        total += n
    print(f"total: {total} lines")


if __name__ == "__main__":
    main()
