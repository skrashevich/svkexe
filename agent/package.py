#!/usr/bin/env python3
"""Export the complete source package without build outputs or parent files."""

from pathlib import Path
import tarfile


root = Path(__file__).resolve().parent
output = root / "bin/picoclaw-agent-source.tar.gz"
output.parent.mkdir(parents=True, exist_ok=True)
entries = (
    ".gitignore", ".gitattributes", "Makefile", "README.md", "build.sh", "configure.py", "package.py",
    "upstream.env", "go.mod.lock", "go.sum.lock", "runtime.patch",
    "overlay", "profiles", "licenses", "tests",
)
with tarfile.open(output, "w:gz") as archive:
    for entry in entries:
        path = root / entry
        files = sorted(path.rglob("*")) if path.is_dir() else [path]
        for file in files:
            if file.is_file() and "__pycache__" not in file.parts and file.suffix != ".pyc":
                archive.add(file, arcname=Path("agent") / file.relative_to(root), recursive=False)
print(output)
