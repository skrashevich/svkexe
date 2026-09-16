#!/usr/bin/env python3
"""Apply package/profile overlays and safely encode the application name."""

import html
import json
from pathlib import Path
import shutil
import sys


def configure(source, profile, name):
    package = Path(__file__).resolve().parent
    overlays = [package / "overlay", package / "profiles" / profile / "overlay"]
    for overlay in overlays:
        for template in sorted(overlay.rglob("*.in")):
            destination = source / template.relative_to(overlay).with_suffix("")
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(template, destination)

    manifest = source / "ui/src/assets/manifest.json"
    content = json.loads(manifest.read_text())
    content.update(name=name, short_name=name)
    manifest.write_text(json.dumps(content, ensure_ascii=False, indent=2) + "\n")

    index = source / "ui/src/index.html"
    content = index.read_text()
    content = content.replace('content="PicoClaw"', f'content="{html.escape(name, quote=True)}"')
    content = content.replace("<title>PicoClaw</title>", f"<title>{html.escape(name)}</title>")
    index.write_text(content)

    app = source / "ui/src/vue/App.vue"
    content = app.read_text()
    # A literal </script> would terminate Vue's script block even inside a string.
    literal = json.dumps(name).replace("<", "\\u003c")
    app.write_text(content.replace('parts.push("PicoClaw");', f"parts.push({literal});"))


if __name__ == "__main__":
    configure(Path(sys.argv[1]), sys.argv[2], sys.argv[3])
