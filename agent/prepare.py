#!/usr/bin/env python3
"""Prepare a build directory from the vendored Shelley tree.

The build never runs inside shelley/ itself: that tree is tracked source, and
a build writes generated files (UI bundle, template tarballs, exe-scroll
binaries) and rewrites the branded UI files. Instead the tree is synced into a
disposable directory where the profile overlay and application name are
applied.

Only files whose content differs are written, and files that only exist in the
destination are removed. Unchanged files keep their modification time on
purpose: the agent binary refuses to start when any file under ui/src is newer
than the embedded UI bundle, so rewriting untouched files would invalidate
every binary built from the directory before.

Usage: prepare.py DEST PROFILE NAME
Prints a digest of the prepared UI inputs, which the build uses to decide
whether the UI bundle must be rebuilt.
"""

import hashlib
import html
import json
import os
from pathlib import Path, PurePosixPath
import sys

PACKAGE = Path(__file__).resolve().parent
SOURCE = PACKAGE / "shelley"

# Marks a directory as one this script may overwrite and prune. Hosts that
# built before the source was vendored carry the older marker name.
MARKER = ".picoclaw-fingerprint"
LEGACY_MARKERS = (".svkexe-fingerprint",)

# Written by builds and tests inside a Shelley tree; never source, never synced,
# never pruned from a destination. CI checks that this list agrees with the
# tracked tree, because a file dropped here would silently miss every build.
GENERATED_DIRS = (
    ".git",
    ".omc",
    ".it",
    "ui/node_modules",
    "ui/dist",
    "ui/.next",
    "ui/out",
    "ui/test-results",
    "ui/playwright-report",
)


def is_generated(rel):
    """Report whether a path relative to a Shelley tree is a build product."""
    rel = PurePosixPath(rel)
    posix = rel.as_posix()
    if any(posix == d or posix.startswith(d + "/") for d in GENERATED_DIRS):
        return True
    if "__pycache__" in rel.parts or rel.suffix == ".pyc":
        return True
    if rel.parts[0] == "templates" and rel.suffix == ".gz":
        return True
    # Upstream tracks install scripts under bin/ next to its build outputs.
    if rel.parts[0] == "bin" and rel.name.startswith("shelley"):
        return True
    if posix.startswith("exescroll/binary/") and rel.name != ".gitignore":
        return True
    if rel.name.endswith(".db") or ".db-" in rel.name:
        return True
    return rel.name in (MARKER, ".DS_Store")


def source_files(root=SOURCE):
    """Yield (relative posix path, absolute path) for every source file."""
    for dirpath, dirnames, filenames in os.walk(root):
        base = Path(dirpath)
        rel_base = base.relative_to(root)
        dirnames[:] = sorted(d for d in dirnames if not is_generated(rel_base / d))
        for name in sorted(filenames):
            rel = (rel_base / name).as_posix()
            if not is_generated(rel):
                yield rel, base / name


def branded(rel, data, name):
    """Apply the application name to the UI files that carry it."""
    if rel == "ui/src/assets/manifest.json":
        manifest = json.loads(data)
        manifest.update(name=name, short_name=name)
        return (json.dumps(manifest, ensure_ascii=False, indent=2) + "\n").encode()
    if rel == "ui/src/index.html":
        text = data.decode()
        text = text.replace('content="PicoClaw"', f'content="{html.escape(name, quote=True)}"')
        text = text.replace("<title>PicoClaw</title>", f"<title>{html.escape(name)}</title>")
        return text.encode()
    if rel == "ui/src/vue/App.vue":
        # A literal </script> would terminate Vue's script block even inside a string.
        literal = json.dumps(name).replace("<", "\\u003c")
        return data.decode().replace('parts.push("PicoClaw");', f"parts.push({literal});").encode()
    return data


def prepared_tree(profile, name):
    """Return {relative path: bytes} of the tree a build should see."""
    tree = {}
    for rel, path in source_files():
        tree[rel] = branded(rel, path.read_bytes(), name)
    overlay = PACKAGE / "profiles" / profile / "overlay"
    if overlay.is_dir():
        for template in sorted(overlay.rglob("*.in")):
            rel = template.relative_to(overlay).with_suffix("").as_posix()
            tree[rel] = branded(rel, template.read_bytes(), name)
    return tree


def sync(dest, tree):
    """Make dest contain exactly tree, touching only what differs."""
    markers = (MARKER,) + LEGACY_MARKERS
    if dest.exists() and any(dest.iterdir()) and not any((dest / m).is_file() for m in markers):
        sys.exit(f"Refusing to overwrite {dest}: it was not prepared by this script; delete it to build there")
    # The marker goes first so an interrupted sync leaves a directory the next
    # run may still finish.
    dest.mkdir(parents=True, exist_ok=True)
    (dest / MARKER).write_text("prepared by agent/prepare.py; safe to delete\n")
    # Prune before writing so a path that changed between file and directory
    # is gone before its replacement is created.
    visited = []
    for dirpath, dirnames, filenames in os.walk(dest):
        base = Path(dirpath)
        rel_base = base.relative_to(dest)
        # Build products such as ui/node_modules are neither pruned nor walked.
        dirnames[:] = [d for d in dirnames if not is_generated(rel_base / d)]
        visited.extend(base / d for d in dirnames)
        for name in filenames:
            rel = (rel_base / name).as_posix()
            if rel not in tree and not is_generated(rel):
                (base / name).unlink()
    for directory in reversed(visited):
        if not any(directory.iterdir()):
            directory.rmdir()
    for rel, data in tree.items():
        target = dest / rel
        if target.is_file() and target.read_bytes() == data:
            continue
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
        source = SOURCE / rel
        if source.is_file():
            target.chmod(source.stat().st_mode & 0o777)


def ui_digest(tree):
    """Digest of the UI inputs; the profile overlay and name are already in them."""
    digest = hashlib.sha256()
    for rel in sorted(tree):
        if rel.startswith("ui/"):
            digest.update(rel.encode() + b"\0")
            digest.update(tree[rel])
    return digest.hexdigest()


def main():
    if len(sys.argv) != 4:
        sys.exit(__doc__)
    dest, profile, name = Path(sys.argv[1]).resolve(), sys.argv[2], sys.argv[3]
    if profile != "standalone" and not (PACKAGE / "profiles" / profile).is_dir():
        sys.exit(f"Unknown profile {profile}")
    if dest == SOURCE or SOURCE in dest.parents:
        sys.exit("The build directory must be outside shelley/")
    tree = prepared_tree(profile, name)
    sync(dest, tree)
    print(ui_digest(tree))


if __name__ == "__main__":
    main()
