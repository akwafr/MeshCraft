#!/usr/bin/env python3
"""Fill non-en/fr locale files: replace EN-copy values with real translations."""
import json
import os
import re
import time
import urllib.parse
import urllib.request

LOCALES_DIR = os.path.join(os.path.dirname(__file__), "..", "src", "i18n", "locales")
TRANS_MAP = {
    "de": "de",
    "es": "es",
    "it": "it",
    "pt": "pt",
    "nl": "nl",
    "ru": "ru",
    "ar": "ar",
    "zh": "zh-CN",
    "ja": "ja",
}

TECH_EXACT = {
    "debug", "info", "notice", "warning", "error", "critical",
    "SNMP", "SNMPv2c", "SNMPv3", "VLAN", "VLANs", "LLDP", "PoE", "TRUNK", "DHCP",
    "SSH", "HTTP", "HTTPS", "API", "JSON", "CPU", "NTP", "DNS", "AAA", "BPDU",
    "admin", "never", "active", "disabled", "instance", "proxy", "note", "add",
    "clear", "loading", "name", "save", "edit", "help", "confirm", "cancel",
    "delete", "Dashboard", "Monitoring", "Debug", "Pipelines", "meshcraft",
    "ONLINE", "OFFLINE", "UNKNOWN", "ALERT", "UNREACHABLE", "ACCÈS", "ACCESS",
    "all", "ssh", "http", "telnet", "any", "mcadmin", "nopriv",
    "sha", "sha256", "aes128", "md5", "des", "aes256", "aes192", "sha512",
    "sha384", "sha224", "Enter", "STP", "ID", "OK", "—",
    "free", "stored", "empty", "tried", "online", "revoked", "unknown",
    "Trunk", "Mode", "Tags", "Type", "System", "Uptime", "Link", "Selection",
    "Hostname", "Network", "Address", "State", "Location", "Collector",
    "Description", "Rename", "Device", "Ports", "Profile", "Profiles",
    "Configuration", "Monitoring", "Dashboard", "Sites", "Users", "Proxy",
    "Proxys", "Pipelines", "Debug", "Help", "Confirm", "Cancel", "Delete",
    "Add", "Save", "Edit", "Clear", "Loading", "Name", "Note", "Instance",
    "PoE", "VLAN", "LLDP", "SNMP", "MAC", "IP", "DHCP", "WSS", "syslog",
}


def flatten(obj, prefix=""):
    out = {}
    for k, v in obj.items():
        key = f"{prefix}.{k}" if prefix else k
        if isinstance(v, dict):
            out.update(flatten(v, key))
        else:
            out[key] = v
    return out


def unflatten(flat_map):
    root = {}
    for key, val in flat_map.items():
        parts = key.split(".")
        cur = root
        for p in parts[:-1]:
            if p not in cur:
                cur[p] = {}
            cur = cur[p]
        cur[parts[-1]] = val
    return root


def protect(s):
    tokens = []

    def repl(m):
        tokens.append(m.group(0))
        return f"__TOK{len(tokens)-1}__"

    s2 = re.sub(r"\{\{[^}]+\}\}", repl, s)
    s2 = re.sub(r"<[^>]+>", repl, s2)
    s2 = re.sub(r"meshcraft", repl, s2, flags=re.I)
    s2 = re.sub(r"SNMPv?[23]?c?", repl, s2, flags=re.I)
    return s2, tokens


def unprotect(s, tokens):
    for i, tok in enumerate(tokens):
        s = s.replace(f"__TOK{i}__", tok)
    return s


def is_technical(en_val):
    if not en_val or not isinstance(en_val, str):
        return True
    s = en_val.strip()
    if s in TECH_EXACT:
        return True
    if len(s) <= 2:
        return True
    if re.match(r"^[\d\sW%/.\-—⇧⌘«»]+$", s):
        return True
    # Short labels that are often kept in English in UIs
    if len(s.split()) == 1 and s[0].isupper() and len(s) <= 12:
        if s in TECH_EXACT or s.lower() in {x.lower() for x in TECH_EXACT}:
            return True
    return False


def translate_text(text, dest):
    protected, tokens = protect(text)
    if not protected.strip():
        return text
    if re.match(r"^(__TOK\d__\s*)+$", protected):
        return text
    q = urllib.parse.quote(protected)
    url = (
        f"https://translate.googleapis.com/translate_a/single"
        f"?client=gtx&sl=en&tl={dest}&dt=t&q={q}"
    )
    req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0"})
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            data = json.loads(resp.read().decode())
        out = "".join(part[0] for part in data[0] if part[0])
    except Exception as exc:
        print(f"  translate error ({dest}): {exc}")
        return text
    return unprotect(out, tokens)


def main():
    import sys
    only = sys.argv[1:] if len(sys.argv) > 1 else list(TRANS_MAP.keys())
    with open(os.path.join(LOCALES_DIR, "en.json"), encoding="utf-8") as f:
        en_tree = json.load(f)
    en = flatten(en_tree)

    cache = {}

    for lang, gdest in TRANS_MAP.items():
        if lang not in only:
            continue
        path = os.path.join(LOCALES_DIR, f"{lang}.json")
        with open(path, encoding="utf-8") as f:
            tree = json.load(f)
        cur = flatten(tree)
        updated = 0
        for key, en_val in en.items():
            if key not in cur:
                continue
            if cur[key] != en_val:
                continue
            if is_technical(en_val):
                continue
            cache_key = (gdest, en_val)
            if cache_key not in cache:
                cache[cache_key] = translate_text(en_val, gdest)
                time.sleep(0.08)
            cur[key] = cache[cache_key]
            updated += 1
        with open(path, "w", encoding="utf-8") as f:
            json.dump(unflatten(cur), f, ensure_ascii=False, indent=2)
            f.write("\n")
        same = sum(1 for k in en if cur.get(k) == en.get(k))
        pct = 100 * same / len(en)
        print(f"{lang}: translated {updated}, same_as_en now {same}/{len(en)} ({pct:.1f}%)")

    for lang in ["fr"] + list(TRANS_MAP.keys()):
        with open(os.path.join(LOCALES_DIR, f"{lang}.json"), encoding="utf-8") as f:
            other = flatten(json.load(f))
        missing = set(en.keys()) - set(other.keys())
        extra = set(other.keys()) - set(en.keys())
        if missing or extra:
            print(f"KEY MISMATCH {lang}: missing={len(missing)} extra={len(extra)}")


if __name__ == "__main__":
    main()
