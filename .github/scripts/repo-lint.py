#!/usr/bin/env python3
from __future__ import annotations

import json
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]

# Empty on purpose: bootstrap deletes CHECKLIST.md, the only doc that explained placeholders.
PLACEHOLDER_DOCS: set[str] = set()

PLACEHOLDER_RE = re.compile(r"\{\{[A-Z][A-Z0-9_]*\}\}")
SKIP_DIRS = {".git", "node_modules", "dist", "bin", ".task", "vendor",
             "vulnerability-check"}
KNOWN_SUFFIXES = {".md", ".yml", ".yaml", ".json", ".toml", ".sh", ".go", ".env",
                  ".txt", ".js", ".py", ".mjs", ".cfg", ".ini"}


def _files(*suffixes: str):
    for path in ROOT.rglob("*"):
        if not path.is_file():
            continue
        if SKIP_DIRS & set(path.relative_to(ROOT).parts):
            continue
        if suffixes and path.suffix not in suffixes:
            continue
        yield path


def _rel(path: pathlib.Path) -> str:
    return str(path.relative_to(ROOT))


def check_yaml_loads(errors: list[str]) -> None:
    """yamllint accepts YAML that PyYAML cannot construct, and GitHub then rejects it."""
    try:
        import yaml
    except ImportError:
        errors.append("PyYAML is not installed; cannot verify YAML files")
        return

    for path in _files(".yml", ".yaml"):
        # Go templates, not YAML until rendered; chart-ci.yml lints them.
        if path.is_relative_to(ROOT / "deploy/chart/templates"):
            continue
        try:
            yaml.safe_load(path.read_text(encoding="utf-8"))
        except Exception as exc:  # noqa: BLE001 - report whatever the loader raises
            errors.append(f"{_rel(path)}: does not load as YAML: {type(exc).__name__}: {exc}")


def check_json_loads(errors: list[str]) -> None:
    for path in _files(".json"):
        # Fixtures may be malformed on purpose, and govulncheck fixtures are JSON streams.
        if "lock" in path.name or "testdata" in path.parts:
            continue
        try:
            json.loads(path.read_text(encoding="utf-8"))
        except Exception as exc:  # noqa: BLE001
            errors.append(f"{_rel(path)}: invalid JSON: {exc}")


def check_labeler_labels_declared(errors: list[str]) -> None:
    """actions/labeler silently creates undeclared labels with a random colour and no description."""
    import yaml

    labeler = ROOT / ".github/labeler.yml"
    settings = ROOT / ".github/settings.yml"
    if not (labeler.exists() and settings.exists()):
        return

    declared = set((yaml.safe_load(settings.read_text(encoding="utf-8")) or {}).get("labels") or {})

    sources = {".github/labeler.yml": set(yaml.safe_load(labeler.read_text(encoding="utf-8")) or {})}

    for form in sorted((ROOT / ".github/ISSUE_TEMPLATE").glob("*.y*ml")):
        if form.name == "config.yml":
            continue
        doc = yaml.safe_load(form.read_text(encoding="utf-8")) or {}
        labels = doc.get("labels") or []
        if isinstance(labels, str):
            labels = [part.strip() for part in labels.split(",")]
        if labels:
            sources[_rel(form)] = set(labels)

    for source, emitted in sources.items():
        for name in sorted(emitted - declared):
            errors.append(f"{source} applies label {name!r} which .github/settings.yml does not declare")


def check_release_please_packages_exist(errors: list[str]) -> None:
    """release-please silently tracks package paths that do not exist."""
    config = ROOT / ".release-please/config-app.json"
    manifest = ROOT / ".release-please/manifest-app.json"
    if not config.exists():
        return

    packages = json.loads(config.read_text(encoding="utf-8")).get("packages") or {}
    for pkg in packages:
        if not (ROOT / pkg).is_dir():
            errors.append(f".release-please/config-app.json tracks package {pkg!r}, which is not a directory")

    if manifest.exists():
        tracked = set(json.loads(manifest.read_text(encoding="utf-8")))
        for pkg in sorted(tracked - set(packages)):
            errors.append(f".release-please/manifest-app.json pins {pkg!r}, which config-app.json does not track")
        for pkg in sorted(set(packages) - tracked):
            errors.append(f".release-please/config-app.json tracks {pkg!r}, which manifest-app.json does not pin")


def check_version_file_matches_manifest(errors: list[str]) -> None:
    config = ROOT / ".release-please/config-app.json"
    manifest = ROOT / ".release-please/manifest-app.json"
    version = ROOT / "version.txt"
    if not (config.exists() and manifest.exists() and version.exists()):
        return
    if json.loads(config.read_text(encoding="utf-8")).get("release-type") != "simple":
        return

    pinned = json.loads(manifest.read_text(encoding="utf-8")).get(".")
    actual = version.read_text(encoding="utf-8").strip()
    if pinned != actual:
        errors.append(f"version.txt is {actual!r} but .release-please/manifest-app.json pins {pinned!r}")


def check_referenced_paths_exist(errors: list[str]) -> None:
    """Only links and table-row paths claim a file exists; prose may name optional extras."""
    link = re.compile(r"\]\(([^)#:]+?)\)")
    cell = re.compile(r"`([^`\s]+?\.[A-Za-z0-9]+)`")
    owned = {".github", "docs", "scripts", "packs", "optional", "taskfile", "test", "tests"}

    def flag(path: pathlib.Path, ref: str) -> None:
        ref = ref.strip()
        if ref.startswith("./"):
            ref = ref[2:]
        if not ref or ref.startswith(("http", "mailto", "#", "/")):
            return
        if pathlib.PurePath(ref).suffix not in KNOWN_SUFFIXES:
            return
        # Links resolve relative to the file, table paths usually to the root.
        if (path.parent / ref).exists() or (ROOT / ref).exists():
            return
        errors.append(f"{_rel(path)}: references {ref!r}, which does not exist")

    for path in _files(".md"):
        for line in path.read_text(encoding="utf-8").splitlines():
            for match in link.finditer(line):
                flag(path, match.group(1))
            # A bare `config.yml` is relative to the section heading, so require a directory.
            if line.lstrip().startswith("|"):
                for match in cell.finditer(line):
                    if "/" in match.group(1) and match.group(1).split("/", 1)[0] in owned:
                        flag(path, match.group(1))


def check_local_workflow_calls_resolve(errors: list[str]) -> None:
    workflows = ROOT / ".github/workflows"
    if not workflows.is_dir():
        return

    local = re.compile(r"uses:\s*(\./[A-Za-z0-9._/-]+)")
    for path in sorted(workflows.glob("*.y*ml")):
        for match in local.finditer(path.read_text(encoding="utf-8")):
            ref = match.group(1)[2:]
            target = ROOT / ref
            is_action_dir = target.is_dir() and any(
                (target / name).is_file() for name in ("action.yml", "action.yaml")
            )
            if target.is_file() or is_action_dir:
                continue
            errors.append(f"{_rel(path)}: calls {match.group(1)!r}, which does not exist")


# Anchored on the key so hygiene.yml's own printed pin examples are not read as pins.
USES_RE = re.compile(r"^\s*(?:-\s+)?uses:\s*(?P<ref>\S+)(?:\s*#\s*(?P<comment>.*?))?\s*$")


def check_action_pins_agree(errors: list[str]) -> None:
    """lint:pins checks each `uses:` alone, so a stale branch can reintroduce an old SHA.

    Grouped by owner/repo because subpaths of one repository are one release.
    """
    seen: dict[str, dict[tuple[str, str], list[str]]] = {}

    for root in (ROOT / ".github/workflows", ROOT / ".github/actions"):
        if not root.is_dir():
            continue
        for path in sorted(root.rglob("*")):
            if not path.is_file() or path.suffix not in {".yml", ".yaml"}:
                continue
            for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
                match = USES_RE.match(line)
                if not match:
                    continue
                ref = match.group("ref")
                if ref.startswith(("./", "docker://")) or "@" not in ref:
                    continue
                action, _, sha = ref.rpartition("@")
                # Anything that is not a SHA is lint:pins' complaint, not this one.
                if not re.fullmatch(r"[a-f0-9]{40}", sha):
                    continue
                repo = "/".join(action.split("/")[:2])
                comment = (match.group("comment") or "").strip()
                key = (sha, comment)
                seen.setdefault(repo, {}).setdefault(key, []).append(f"{_rel(path)}:{number}")

    for repo, pins in sorted(seen.items()):
        if len(pins) < 2:
            continue
        detail = "; ".join(
            f"{sha} ({comment or 'no version comment'}) in {', '.join(where)}"
            for (sha, comment), where in sorted(pins.items())
        )
        errors.append(f"{repo} is pinned at {len(pins)} different versions: {detail}")


def check_issue_template_config(errors: list[str]) -> None:
    """One incomplete contact link makes GitHub reject the whole issue chooser."""
    import yaml

    config = ROOT / ".github/ISSUE_TEMPLATE/config.yml"
    if not config.exists():
        return

    doc = yaml.safe_load(config.read_text(encoding="utf-8")) or {}
    for index, link in enumerate(doc.get("contact_links") or []):
        missing = [key for key in ("name", "url", "about") if not (link or {}).get(key)]
        if missing:
            name = (link or {}).get("name", f"#{index}")
            errors.append(
                f".github/ISSUE_TEMPLATE/config.yml: contact link {name!r} is missing {', '.join(missing)}"
            )


def check_pr_target_never_checks_out(errors: list[str]) -> None:
    """pull_request_target has a writable token; checking out the PR runs fork code with it."""
    import yaml

    workflows = ROOT / ".github/workflows"
    if not workflows.is_dir():
        return

    for path in sorted(workflows.glob("*.y*ml")):
        try:
            doc = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
        except Exception:
            continue  # check_yaml_loads already reported it

        # PyYAML reads unquoted `on` as True. Handle mapping, list and string forms,
        # or the workflow silently passes.
        triggers = doc.get("on", doc.get(True))
        if isinstance(triggers, dict):
            names = set(triggers)
        elif isinstance(triggers, list):
            names = set(triggers)
        elif isinstance(triggers, str):
            names = {triggers}
        else:
            names = set()
        if "pull_request_target" not in names:
            continue

        for job in (doc.get("jobs") or {}).values():
            for step in (job or {}).get("steps") or []:
                uses = (step or {}).get("uses", "")
                if isinstance(uses, str) and uses.startswith("actions/checkout"):
                    errors.append(
                        f"{_rel(path)}: pull_request_target workflow checks out code "
                        f"({uses}); that runs fork code with a writable token"
                    )


MARKER_RES = (
    re.compile(r"<!--\s*template-only:(?:start|end)\s*-->"),
    re.compile(r"<!--\s*(?:if:[A-Z_]+|endif)\s*-->"),
    re.compile(r"^\s*#\s*(?:if:[A-Z_]+|endif)\s*$", re.M),
)


BINARY_MAGIC = (
    b"\x7fELF",          # ELF
    b"\xfe\xed\xfa\xce",  # Mach-O 32
    b"\xfe\xed\xfa\xcf",  # Mach-O 64
    b"\xce\xfa\xed\xfe",  # Mach-O 32, byte-swapped
    b"\xcf\xfa\xed\xfe",  # Mach-O 64, byte-swapped
    b"\xca\xfe\xba\xbe",  # Mach-O universal
    b"MZ",                 # PE
)


def check_base_image_pin_matches(errors: list[str]) -> None:
    """The copy Renovate updates is not necessarily the one the build uses."""
    env_file = ROOT / "versions.env"
    dockerfile = ROOT / "Dockerfile"
    if not (env_file.exists() and dockerfile.exists()):
        return

    env = dict(
        line.split("=", 1)
        for line in env_file.read_text(encoding="utf-8").splitlines()
        if "=" in line and not line.lstrip().startswith("#")
    )
    text = dockerfile.read_text(encoding="utf-8")

    for key in ("BASE_IMAGE", "BASE_IMAGE_DIGEST"):
        if key not in env:
            continue
        match = re.search(rf"^ARG {key}=(.+)$", text, re.M)
        if not match:
            errors.append(f"Dockerfile: no `ARG {key}=` to compare against versions.env")
        elif match.group(1).strip() != env[key].strip():
            errors.append(
                f"Dockerfile `ARG {key}={match.group(1).strip()}` does not match "
                f"versions.env `{key}={env[key].strip()}`"
            )


def check_no_committed_binaries(errors: list[str]) -> None:
    try:
        tracked = subprocess.run(
            ["git", "-C", str(ROOT), "ls-files", "-z"],
            capture_output=True, check=True,
        ).stdout.split(b"\0")
    except (subprocess.CalledProcessError, FileNotFoundError, OSError):
        return  # not a git checkout; nothing to assert

    for raw in tracked:
        if not raw:
            continue
        rel = raw.decode("utf-8", "replace")
        path = ROOT / rel
        if not path.is_file() or path.is_symlink():
            continue
        with path.open("rb") as handle:
            head = handle.read(4)
        if any(head.startswith(magic) for magic in BINARY_MAGIC):
            size = path.stat().st_size
            errors.append(f"{rel}: a compiled executable is committed ({size} bytes)")


def check_bootstrap_left_nothing_behind(errors: list[str]) -> None:
    if (ROOT / "CHECKLIST.md").exists():
        return  # still an unadopted template

    # Must cover every file bootstrap rewrites.
    extra = {"Dockerfile", "LICENSE", "NOTICE", "CODEOWNERS", "go.mod", ".gitignore"}
    candidates = [
        path for path in _files()
        if path.suffix in {".md", ".yml", ".yaml", ".json", ".toml", ".txt", ".env", ".go", ".py"}
        or path.name in extra
    ]

    for path in candidates:
        rel = _rel(path)
        if rel in PLACEHOLDER_DOCS:
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue

        found = sorted(set(PLACEHOLDER_RE.findall(text)))
        if found:
            errors.append(f"{rel}: unreplaced placeholder(s): {', '.join(found)}")

        for pattern in MARKER_RES:
            for match in pattern.findall(text):
                errors.append(f"{rel}: bootstrap marker left behind: {match.strip()!r}")


CHECKS = (
    check_yaml_loads,
    check_json_loads,
    check_labeler_labels_declared,
    check_release_please_packages_exist,
    check_version_file_matches_manifest,
    check_referenced_paths_exist,
    check_pr_target_never_checks_out,
    check_action_pins_agree,
    check_issue_template_config,
    check_local_workflow_calls_resolve,
    check_bootstrap_left_nothing_behind,
    check_no_committed_binaries,
    check_base_image_pin_matches,
)


def main() -> int:
    errors: list[str] = []
    for check in CHECKS:
        try:
            check(errors)
        except Exception as exc:  # noqa: BLE001 - a broken check must not pass silently
            errors.append(f"{check.__name__} raised {type(exc).__name__}: {exc}")

    if errors:
        print(f"repo-lint: {len(errors)} problem(s)\n", file=sys.stderr)
        for err in errors:
            print(f"  - {err}", file=sys.stderr)
        return 1

    print(f"repo-lint: {len(CHECKS)} checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
