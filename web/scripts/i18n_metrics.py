#!/usr/bin/env python3
"""Report i18n metrics: key count, same_as_en %, residual FR."""
import json
import os
import re
from pathlib import Path

LOCALES_DIR = Path(__file__).resolve().parent.parent / "src" / "i18n" / "locales"
SRC = LOCALES_DIR.parent.parent
LANGS = ["fr", "de", "es", "it", "pt", "nl", "ru", "ar", "zh", "ja"]

FR_ACCENT = re.compile(r"[àâäéèêëïîôùûüçÀÂÄÉÈÊËÏÎÔÙÛÜÇ]")
FR_WORDS = re.compile(
    r'(?:>\s*|["\'])[^"\']*\b(Aucun|Équipement|Politiques|Configurer|Enregistrer|Révoquer|Générer|Télécharger|Renommer|Migrer|Plages|Binaire|ALIMENTÉ|COUPÉ|étiquette|équipement|communauté)\b',
    re.I,
)
SKIP_LINE = re.compile(
    r"\b(secret|secret2|plages|Français|Português|phrase|linkPrefix)\b",
    re.I,
)


def flatten(obj, prefix=""):
    out = {}
    for k, v in obj.items():
        key = f"{prefix}.{k}" if prefix else k
        if isinstance(v, dict):
            out.update(flatten(v, key))
        else:
            out[key] = v
    return out


def strip_comments(text):
    text = re.sub(r"/\*[\s\S]*?\*/", "", text)
    text = re.sub(r"//[^\n]*", "", text)
    text = re.sub(r"\{/\*[\s\S]*?\*/\}", "", text)
    return text


def main():
    with open(LOCALES_DIR / "en.json", encoding="utf-8") as f:
        en = flatten(json.load(f))

    print(f"en.json keys: {len(en)}")
    print("\nsame_as_en % per locale:")
    for lang in LANGS:
        with open(LOCALES_DIR / f"{lang}.json", encoding="utf-8") as f:
            other = flatten(json.load(f))
        missing = set(en) - set(other)
        extra = set(other) - set(en)
        same = sum(1 for k in en if other.get(k) == en.get(k))
        pct = 100.0 * same / len(en) if en else 0
        note = ""
        if missing:
            note += f" MISSING {len(missing)}"
        if extra:
            note += f" EXTRA {len(extra)}"
        print(f"  {lang}: {same}/{len(en)} = {pct:.1f}%{note}")

    pages = list((SRC / "pages").rglob("*.tsx"))
    no_i18n = [str(p.relative_to(SRC)) for p in pages if "useTranslation" not in p.read_text(encoding="utf-8")]
    print(f"\npages without useTranslation: {len(no_i18n)}")
    for p in no_i18n:
        print(f"  {p}")

    fr_hits = []
    for path in SRC.rglob("*"):
        if path.suffix not in (".tsx", ".ts") or "locales" in path.parts:
            continue
        text = path.read_text(encoding="utf-8", errors="replace")
        code = strip_comments(text)
        for i, line in enumerate(code.splitlines(), 1):
            if SKIP_LINE.search(line):
                continue
            if FR_ACCENT.search(line) or FR_WORDS.search(line):
                fr_hits.append(f"{path.relative_to(SRC)}:{i}: {line.strip()[:100]}")
    print(f"\nresidual FR outside fr.json (non-comment lines): {len(fr_hits)}")
    for h in fr_hits[:30]:
        print(f"  {h}")
    if len(fr_hits) > 30:
        print(f"  ... and {len(fr_hits) - 30} more")


if __name__ == "__main__":
    main()
