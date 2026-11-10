#!/usr/bin/env python3
"""Check publishable Git files without printing secret values.

Run from any directory. Use --staged to inspect the index instead of the worktree.
Gitleaks runs too when available on PATH or in bin/security-tools/.
"""

import argparse
import os
import re
import shutil
import subprocess
from pathlib import Path
from urllib.parse import unquote, urlsplit


ROOT = Path(__file__).resolve().parent.parent
SECRET_NAME = re.compile(r"secret|token|password|passwd|api_?key|private_?key", re.I)
LOCAL_PATH = re.compile(r"/(?:home|Users)/[^/\s\"']+/|/(?:8tbsata|2tbsata)/|[A-Za-z]:\\(?:Users|Documents and Settings)\\", re.I)


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT)


def local_secrets():
    values = {}
    for name in (".env", ".env-cli", ".token"):
        path = ROOT / name
        if not path.is_file():
            continue
        for line in path.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            if name == ".token":
                values.setdefault(line, set()).add(name)
                continue
            key, separator, value = line.partition("=")
            if not separator:
                continue
            value = value.strip().strip("\"'")
            if SECRET_NAME.search(key) and len(value) >= 12:
                values.setdefault(value, set()).add(f"{name}:{key.strip()}")
            if key.strip() == "DATABASE_URL":
                password = urlsplit(value).password
                if password and len(password) >= 12:
                    values.setdefault(unquote(password), set()).add(f"{name}:DATABASE_URL password")
    for key, value in os.environ.items():
        if key.startswith(("AKHFLOW_", "MCP_", "JWT_")) and SECRET_NAME.search(key) and len(value) >= 12:
            values.setdefault(value, set()).add(f"environment:{key}")
    return {value.encode(): sorted(labels) for value, labels in values.items()}


def sensitive_file(name):
    path = Path(name)
    if path.name == ".env.example":
        return False
    if path.name.startswith(".env") or path.name in (".token", "credentials.json", "id_rsa", "id_ed25519"):
        return True
    if path.suffix.lower() in (".pem", ".key", ".p12", ".pfx"):
        return True
    return bool({".ssh", "secrets"}.intersection(path.parts))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--staged", action="store_true")
    args = parser.parse_args()
    options = ["ls-files", "--cached", "-z"]
    if not args.staged:
        options.extend(["--others", "--exclude-standard"])
    names = sorted(set(os.fsdecode(name) for name in git(*options).split(b"\0") if name))
    secrets = local_secrets()
    blobs = []
    problems = []
    for name in names:
        if sensitive_file(name):
            problems.append(f"{name}: sensitive filename")
        if args.staged:
            data = git("show", f":{name}")
        else:
            path = ROOT / name
            if not path.exists():
                continue
            if path.is_symlink():
                problems.append(f"{name}: review symlink before publishing")
                continue
            data = path.read_bytes()
        blobs.append(data)
        for secret, labels in secrets.items():
            if secret in data:
                problems.append(f"{name}: contains a local secret value ({', '.join(labels)})")
        text = data.decode("utf-8", errors="replace")
        for number, line in enumerate(text.splitlines(), 1):
            if LOCAL_PATH.search(line):
                problems.append(f"{name}:{number}: personal absolute path")
    for problem in problems:
        print(f"FAIL: {problem}", flush=True)
    if problems:
        return 1
    print(f"PASS: {len(blobs)} Git files; no local secret values or personal absolute paths", flush=True)
    scanner = shutil.which("gitleaks")
    if scanner is None:
        local_scanner = ROOT / "bin" / "security-tools" / "gitleaks"
        if local_scanner.is_file():
            scanner = str(local_scanner)
    if scanner is None:
        print("FAIL: install Gitleaks to complete the token and private-key scan")
        return 1
    result = subprocess.run([scanner, "stdin", "--redact", "--no-banner"], input=b"\n".join(blobs), cwd=ROOT)
    return result.returncode


if __name__ == "__main__":
    raise SystemExit(main())
