#!/usr/bin/env python3
"""Export the complete source package without build outputs or parent files."""

from pathlib import Path
import tarfile

from prepare import source_files


root = Path(__file__).resolve().parent
output = root / "bin/picoclaw-agent-source.tar.gz"
output.parent.mkdir(parents=True, exist_ok=True)
entries = (
    ".gitignore", "Makefile", "README.md", "build.sh", "prepare.py", "package.py",
    "upstream.env", "profiles", "licenses", "tests",
)
with tarfile.open(output, "w:gz") as archive:
    for entry in entries:
        path = root / entry
        files = sorted(path.rglob("*")) if path.is_dir() else [path]
        for file in files:
            if file.is_file() and "__pycache__" not in file.parts and file.suffix != ".pyc":
                archive.add(file, arcname=Path("agent") / file.relative_to(root), recursive=False)
    for rel, file in source_files():
        archive.add(file, arcname=Path("agent/shelley") / rel, recursive=False)
print(output)
