# Reads the facts aval gen python needs, with Python's own parsers: each
# manifest through tomllib, each source file through ast. It runs in the
# contract root. Input on stdin: {"manifests": [path], "files": [path]}.
# Output on stdout: {"manifests": {path: toml}, "files": {path: {"imports":
# [name], "dynamic": [line]}}}. Any error exits non-zero with one line.
import ast
import json
import sys

if sys.version_info < (3, 11):
    sys.exit("aval gen python needs Python 3.11 or later (tomllib)")
import tomllib


def imports(tree):
    names, dynamic = [], []
    for n in ast.walk(tree):
        if isinstance(n, ast.Import):
            names += [a.name for a in n.names]
        elif isinstance(n, ast.ImportFrom):
            names.append("." * n.level + (n.module or ""))
        elif isinstance(n, ast.Call) and (
            getattr(n.func, "id", None) in ("__import__", "import_module")
            or getattr(n.func, "attr", None) == "import_module"
        ):
            arg = n.args[0] if n.args else None
            if isinstance(arg, ast.Constant) and isinstance(arg.value, str):
                names.append(arg.value)
            else:
                dynamic.append(n.lineno)
    return {"imports": names, "dynamic": dynamic}


req = json.load(sys.stdin)
out = {"manifests": {}, "files": {}}
path = "stdin"
try:
    for path in req["manifests"]:
        with open(path, "rb") as f:
            out["manifests"][path] = tomllib.load(f)
    for path in req["files"]:
        with open(path, "rb") as f:
            out["files"][path] = imports(ast.parse(f.read(), path))
except Exception as e:
    sys.exit(f"{path}: {e}")
json.dump(out, sys.stdout, default=str)
