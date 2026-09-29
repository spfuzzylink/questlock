#!/usr/bin/env python3
"""Generate reproducible notices from the four production Go dependency graphs."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
TARGETS = ("darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64")
NOTICE_NAME = re.compile(r"^(?:LICEN[CS]E|COPYING|COPYRIGHT|NOTICES?|PATENTS)(?:[._-].+)?$", re.I)
SOURCE_TOKEN = re.compile(r'//[^\n]*|/\*[\s\S]*?\*/|"(?:\\[\s\S]|[^"\\])*"|\'(?:\\[\s\S]|[^\'\\])*\'|`[^`]*`')
LEGAL_COMMENT = re.compile(r"copyright|SPDX-License-Identifier:|redistribution and use|permission is hereby granted|licensed under|public domain|general public license|apple public source license", re.I)
# These upstream files describe optional assets or a larger module graph, not
# the packages selected by our production build. Keep exclusions explicit so
# changes to the dependency graph or upstream notice files are reviewable.
EXCLUDED_MODULE_FILES = {
    "modernc.org/sqlite": {
        "LICENSE-3RD-PARTY.md": "upstream's whole module graph, including test/build dependencies",
        "LICENSE-SQLITE_VEC": "optional sqlite-vec extension, not imported by Questlock",
    },
    "modernc.org/memory": {"LICENSE-LOGO": "unbundled logo asset"},
}
# Exact upstream/SPDX snapshots for license references found in the selected
# translated-source comments. Generation never fetches these from the network.
SUPPLEMENTAL_TEXTS = (
    ("LLVM-LICENSE.txt", "https://llvm.org/LICENSE.txt",
     "8d85c1057d742e597985c7d4e6320b015a9139385cff4cbae06ffc0ebe89afee", "LLVM"),
    ("APSL-1.1.txt", "https://raw.githubusercontent.com/spdx/license-list-data/v3.27.0/text/APSL-1.1.txt",
     "81757b1a656d5e762711d6e45395fe9772db5058fc962c8f8a1b64b9d29fb9ac", "Apple Public Source License"),
    ("APSL-2.0.txt", "https://raw.githubusercontent.com/spdx/license-list-data/v3.27.0/text/APSL-2.0.txt",
     "2d0c12edaaf8d31fec6796fef99291b12e833b6bef28100975120a53d209e06d", "Apple Public Source License"),
    ("GPL-3.0.txt", "https://raw.githubusercontent.com/gcc-mirror/gcc/releases/gcc-14.2.0/COPYING3",
     "8ceb4b9ee5adedde47b31e975c1d90c73ad27b6b165a1dcd80c7c545eb65b903", "GCC Runtime Library Exception"),
    ("GCC-RUNTIME-EXCEPTION-3.1.txt", "https://raw.githubusercontent.com/gcc-mirror/gcc/releases/gcc-14.2.0/COPYING.RUNTIME",
     "9d6b43ce4d8de0c878bf16b54d8e7a10d9bd42b75178153e3af6a815bdc90f74", "GCC Runtime Library Exception"),
    ("LGPL-2.1.txt", "https://www.gnu.org/licenses/old-licenses/lgpl-2.1.txt",
     "20e50fe7aae3e56378ebf0417d9de904f55a0e61e4df315333e632a4d3555d95", "Lesser General Public"),
)


class NoticeError(Exception):
    pass


def parse_go_json(data):
    """go list emits consecutive JSON objects rather than one JSON array."""
    decoder = json.JSONDecoder()
    packages = []
    offset = 0
    while offset < len(data):
        while offset < len(data) and data[offset].isspace():
            offset += 1
        if offset == len(data):
            break
        package, offset = decoder.raw_decode(data, offset)
        if not isinstance(package, dict):
            raise NoticeError("go list returned a non-object package")
        packages.append(package)
    return packages


def build_environment(target=None):
    env = dict(os.environ, CGO_ENABLED="0", GOFLAGS="", GOWORK="off", GOEXPERIMENT="",
               GOAMD64="v1", GOARM64="v8.0")
    if target:
        env["GOOS"], env["GOARCH"] = target.split("/")
    return env


def inspect_go(go):
    try:
        metadata = json.loads(subprocess.check_output(
            [go, "env", "-json", "GOROOT", "GOVERSION"], cwd=ROOT,
            env=build_environment(), text=True, stderr=subprocess.PIPE))
        graphs = {}
        for target in TARGETS:
            data = subprocess.check_output(
                [go, "list", "-deps", "-json", "-mod=readonly", "./cmd/questlock"],
                cwd=ROOT, env=build_environment(target), text=True, stderr=subprocess.PIPE)
            graphs[target] = parse_go_json(data)
        return graphs, Path(metadata["GOROOT"]), metadata["GOVERSION"]
    except (OSError, subprocess.CalledProcessError, ValueError, KeyError) as error:
        # Tool output can contain checkout/cache paths. Keep the public command's
        # errors concise; ordinary go list provides local diagnostics if needed.
        raise NoticeError("cannot inspect production Go dependencies; check the Go toolchain and downloaded modules") from error


def ancestor_notices(directory, root):
    """Find notice files in a selected package and its enclosing component."""
    directory, root = Path(directory), Path(root)
    if directory != root and root not in directory.parents:
        raise NoticeError("a package directory is outside its declared component")
    found = set()
    while True:
        for path in directory.iterdir():
            if NOTICE_NAME.fullmatch(path.name):
                if path.is_symlink():
                    raise NoticeError("notice inputs must not be symlinks")
                if path.is_file():
                    found.add(path)
        if directory == root:
            return found
        directory = directory.parent


def read_notice(path):
    if path.is_symlink() or not path.is_file():
        raise NoticeError("a required upstream notice is missing or symlinked")
    try:
        text = path.read_bytes().decode("utf-8")
    except (OSError, UnicodeDecodeError) as error:
        raise NoticeError("an upstream notice cannot be read as UTF-8 without alteration") from error
    if not text.strip():
        raise NoticeError("an upstream notice is empty")
    return text


def verbatim(text):
    # Preserve original notice bytes inside a fence that cannot be closed by
    # upstream Markdown. A missing terminal newline is added outside the text.
    runs = [len(run) for run in re.findall(r"`+", text)]
    fence = "`" * max(3, max(runs, default=0) + 1)
    return fence + "text\n" + text + ("" if text.endswith("\n") else "\n") + fence + "\n\n"


def legal_comments(source):
    """Extract complete legal comment blocks, excluding Go string literals."""
    blocks = []
    pending = None
    for token in SOURCE_TOKEN.finditer(source):
        text = token.group()
        if not text.startswith(("//", "/*")):
            continue
        if text.startswith("//") and pending and pending[2] and re.fullmatch(r"\r?\n[ \t]*", source[pending[1]:token.start()]):
            pending = (pending[0], token.end(), True)
        else:
            if pending:
                blocks.append(source[pending[0]:pending[1]])
            pending = (token.start(), token.end(), text.startswith("//"))
    if pending:
        blocks.append(source[pending[0]:pending[1]])
    return [block for block in blocks if LEGAL_COMMENT.search(block)]


def selected_sources(package, root):
    files = set()
    for field in ("GoFiles", "CgoFiles", "CFiles", "CXXFiles", "MFiles", "HFiles", "SFiles"):
        for name in package.get(field, []):
            path = Path(package["Dir"]) / name
            if root not in path.parents or path.is_symlink():
                raise NoticeError("a selected source file is outside its component or symlinked")
            files.add(path)
    return files


def source_provenance(files, root):
    blocks = {}
    for path in sorted(files):
        try:
            source = path.read_bytes().decode("utf-8")
        except (OSError, UnicodeDecodeError) as error:
            raise NoticeError("a selected source file cannot be read without alteration") from error
        for block in legal_comments(source):
            blocks.setdefault(block, set()).add(path.relative_to(root).as_posix())
    output = []
    for block, references in sorted(blocks.items(), key=lambda item: (sorted(item[1])[0], item[0])):
        output.append("Source files: " + ", ".join("`" + name + "`" for name in sorted(references)) + ".\n\n")
        output.append(verbatim(block))
    return "".join(output)


def render_notices(graphs, goroot, go_version):
    modules = {}
    runtime_sources = set()
    runtime_notices = {goroot / "LICENSE"}
    if (goroot / "PATENTS").is_file():
        runtime_notices.add(goroot / "PATENTS")
    for target in sorted(graphs):
        for package in graphs[target]:
            if package.get("ForTest") or package.get("Error") or package.get("DepsErrors"):
                raise NoticeError("only complete non-test production dependency graphs are supported")
            module = package.get("Module")
            if module:
                if module.get("Main"):
                    continue
                if module.get("Replace") or not module.get("Version"):
                    raise NoticeError("replaced or unversioned dependencies need an explicit notice review")
                key = (module["Path"], module["Version"])
                root = Path(module["Dir"])
                record = modules.setdefault(key, {"root": root, "targets": set(), "files": set(), "sources": set()})
                if record["root"] != root:
                    raise NoticeError("a module resolved to inconsistent source directories")
                record["targets"].add(target)
                record["files"].update(ancestor_notices(package["Dir"], root))
                record["sources"].update(selected_sources(package, root))
            elif package.get("Standard") and package.get("Dir"):
                notices = ancestor_notices(package["Dir"], goroot)
                boring_license = goroot / "src/crypto/internal/boring/LICENSE"
                if boring_license in notices:
                    # CGO=0 builds use Go's notboring implementation. BoringSSL
                    # object-code terms apply only to the separate boringcrypto
                    # configuration, not these four builds.
                    if package.get("ImportPath") == "crypto/internal/boring" and "notboring.go" not in package.get("GoFiles", []):
                        raise NoticeError("BoringSSL build selection changed; review its additional licensing")
                    if package.get("SysoFiles"):
                        raise NoticeError("a BoringSSL object was selected; review its additional licensing")
                    notices.remove(boring_license)
                runtime_notices.update(notices)
                runtime_sources.update(selected_sources(package, goroot))

    lines = [
        "# Third-party notices\n\n",
        "Questlock's own source is licensed under the repository's `LICENSE`. "
        "Its dependencies retain their respective licenses and copyright notices below.\n\n",
        "Generated by `python3 scripts/generate-notices.py` from the union of "
        "`go list -deps -json -mod=readonly ./cmd/questlock` for the four production targets, "
        "with `CGO_ENABLED=0`, no additional build tags, and default Go experiments. "
        "Test-only and build-tool dependencies are excluded. This is a package-level "
        "inventory; it does not assert that every function in an imported package survives linking.\n\n",
        "Targets: " + ", ".join("`" + target + "`" for target in sorted(graphs)) + ".\n\n",
        "Go toolchain and standard library: `" + go_version + "`. "
        "Regenerate and review this file when the toolchain, dependencies, targets, or build configuration changes.\n\n",
        "Upstream notice texts are reproduced verbatim. Supplemental upstream notices "
        "may describe portions of a component more broadly than the code used by Questlock. "
        "The SQLite module's full test/build dependency inventory, its unimported sqlite-vec "
        "extension, and the memory module's logo notice are excluded.\n\n",
        "The final source-provenance section also preserves legal comment blocks from selected "
        "production source files, including notices transcribed from C headers. Such a comment "
        "does not establish that the associated upstream implementation is present in the linked binary. "
        "These notices are retained conservatively and do not change the licenses stated by their authors.\n\n",
        "## Included Go modules\n\n",
        "| Module | Version | Production targets |\n| --- | --- | --- |\n",
    ]
    for (name, version), record in sorted(modules.items()):
        lines.append("| `" + name + "` | `" + version + "` | " + ", ".join(sorted(record["targets"])) + " |\n")
    lines.append("\n## Go toolchain and standard-library notices\n\n")
    for path in sorted(runtime_notices, key=lambda path: path.relative_to(goroot).as_posix()):
        lines.append("### `" + path.relative_to(goroot).as_posix() + "`\n\n")
        lines.append(verbatim(read_notice(path)))
    for (name, version), record in sorted(modules.items()):
        lines.append("## `" + name + "@" + version + "`\n\n")
        files = {path for path in record["files"]
                 if path.relative_to(record["root"]).as_posix() not in EXCLUDED_MODULE_FILES.get(name, {})}
        if not any(path.name.upper().startswith(("LICENSE", "LICENCE", "COPYING")) for path in files):
            raise NoticeError("a production module has no discovered license; inspect its upstream source")
        for path in sorted(files, key=lambda path: path.relative_to(record["root"]).as_posix()):
            lines.append("### `" + path.relative_to(record["root"]).as_posix() + "`\n\n")
            lines.append(verbatim(read_notice(path)))
    provenance = ["## Selected-source legal comments: provenance\n\n",
                  "### Go toolchain and standard library\n\n",
                  source_provenance(runtime_sources, goroot)]
    for (name, version), record in sorted(modules.items()):
        provenance.append("### `" + name + "@" + version + "`\n\n")
        provenance.append(source_provenance(record["sources"], record["root"]))
    provenance = "".join(provenance)
    lines.append(provenance)
    included_supplements = [entry for entry in SUPPLEMENTAL_TEXTS if entry[3] in provenance]
    if included_supplements:
        lines.append("## Full license texts referenced by selected-source comments\n\n")
        lines.append("These pinned upstream texts accompany the source-provenance notices above; "
                     "their inclusion does not assert that all corresponding upstream implementations "
                     "are linked into Questlock.\n\n")
    for name, url, expected_sha256, _ in included_supplements:
        path = ROOT / "scripts/notices" / name
        text = read_notice(path)
        if hashlib.sha256(text.encode("utf-8")).hexdigest() != expected_sha256:
            raise NoticeError("a pinned supplemental license snapshot changed; review its provenance")
        lines.append("### `" + name + "`\n\n")
        lines.append("Source: [upstream license text](" + url + "). "
                     "Snapshot SHA256: `" + expected_sha256 + "`.\n\n")
        lines.append(verbatim(text))
    return "".join(lines).encode("utf-8")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go executable (default: go on PATH)")
    parser.add_argument("--check", action="store_true", help="fail if the committed notices need regeneration")
    args = parser.parse_args()
    output = ROOT / "THIRD_PARTY_NOTICES.md"
    try:
        graphs, goroot, go_version = inspect_go(args.go)
        expected = render_notices(graphs, goroot, go_version)
        if args.check:
            if not output.is_file() or output.read_bytes() != expected:
                raise NoticeError("THIRD_PARTY_NOTICES.md is stale; regenerate and review the upstream notices")
            print("Third-party notices match the production dependencies and Go toolchain.")
        else:
            output.write_bytes(expected)
            print("Generated THIRD_PARTY_NOTICES.md from all four production targets.")
    except (NoticeError, OSError, KeyError) as error:
        message = str(error) if isinstance(error, NoticeError) else "cannot read the required production source metadata"
        print("Notice generation failed: " + message, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
