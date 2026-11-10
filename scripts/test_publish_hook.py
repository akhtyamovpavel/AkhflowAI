#!/usr/bin/env python3
"""Exercise the real pre-commit hook in an isolated repository under bin/."""

import os
import secrets
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


def main():
    scanner = shutil.which("gitleaks")
    if scanner is None:
        scanner = str(ROOT / "bin" / "security-tools" / "gitleaks")
    if not Path(scanner).is_file():
        raise SystemExit("Gitleaks is required")
    test_root = ROOT / "bin" / "security-tools"
    test_root.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="hook-test-", dir=test_root) as directory:
        repository = Path(directory)
        env = dict(os.environ)
        for key in list(env):
            if key.startswith("GIT_"):
                del env[key]
        env["PYTHON_BIN"] = sys.executable
        redacted_values = []

        def run(*args):
            return subprocess.run(args, cwd=repository, env=env, capture_output=True, text=True)

        def git(*args):
            result = run("git", *args)
            if result.returncode:
                raise AssertionError(result.stderr)
            return result

        def commit(label, expected, marker):
            result = run("git", "-c", "commit.gpgSign=false", "commit", "-m", "Hook fixture")
            output = result.stdout + result.stderr
            succeeded = result.returncode == 0
            assert succeeded == expected, f"{label}: unexpected commit result\n{output}"
            assert marker in output, f"{label}: missing result marker\n{output}"
            for value in redacted_values:
                assert value not in output, f"{label}: secret value appeared in hook output"
            print(f"PASS: {label}")

        git("init", "-b", "main")
        git("config", "user.name", "Hook fixture")
        git("config", "user.email", "hook@example.com")
        git("config", "core.hooksPath", ".githooks")
        (repository / "scripts").mkdir()
        (repository / ".githooks").mkdir()
        scanner_dir = repository / "bin" / "security-tools"
        scanner_dir.mkdir(parents=True)
        scanner_link = scanner_dir / "gitleaks"
        scanner_link.symlink_to(scanner)
        shutil.copy2(ROOT / "scripts" / "check_publish.py", repository / "scripts" / "check_publish.py")
        shutil.copy2(ROOT / ".githooks" / "pre-commit", repository / ".githooks" / "pre-commit")
        (repository / ".gitignore").write_text("bin/\n.env\n.env.production\n")
        document = repository / "sample.txt"
        personal_path = "/" + "home" + "/hook-fixture/private/file"
        document.write_text("Safe staged content\n")
        git("add", "sample.txt")
        document.write_text(f"Unstaged path: {personal_path}\n")
        commit("clean index passes despite an unstaged personal path", True, "no leaks found")

        document.write_text(f"Staged path: {personal_path}\n")
        git("add", "sample.txt")
        document.write_text("Safe unstaged content\n")
        commit("staged personal path blocks a commit", False, "personal absolute path")
        git("reset", "--mixed", "HEAD")

        local_token = "fixture-only-" + secrets.token_hex(24)
        redacted_values.append(local_token)
        (repository / ".env").write_text(f"AKHFLOW_TOKEN={local_token}\n")
        document.write_text(local_token + "\n")
        git("add", "sample.txt")
        commit("local secret blocks a commit without printing its value", False, "local secret value")
        git("reset", "--mixed", "HEAD")

        provider_token = "ghp_" + secrets.token_hex(18)
        redacted_values.append(provider_token)
        document.write_text(f'client_token = "{provider_token}"\n')
        git("add", "sample.txt")
        commit("Gitleaks blocks a synthetic provider token", False, "leaks found")
        git("reset", "--mixed", "HEAD")

        (repository / ".env.production").write_text("SAFE_EXAMPLE=1\n")
        git("add", "-f", ".env.production")
        commit("sensitive filename blocks a commit", False, "sensitive filename")
        git("reset", "--mixed", "HEAD")

        document.write_text("Another safe version\n")
        git("add", "sample.txt")
        scanner_link.unlink()
        env["PATH"] = "/usr/bin:/bin"
        commit("missing Gitleaks blocks a commit", False, "install Gitleaks")
        scanner_link.symlink_to(scanner)
        commit("clean content passes after failures", True, "no leaks found")


if __name__ == "__main__":
    main()
