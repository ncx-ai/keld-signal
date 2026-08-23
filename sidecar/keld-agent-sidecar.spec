# PyInstaller spec — build with: pyinstaller sidecar/keld-agent-sidecar.spec
# One-dir keeps torch's shared libs + data files intact (one-file unpacks slowly
# and is fragile with torch). Produces dist/keld-agent-sidecar/keld-agent-sidecar.
import os

from PyInstaller.utils.hooks import collect_all, collect_submodules

# PyInstaller resolves relative paths in a spec against the spec's own directory,
# so anchor to SPECPATH (…/sidecar) to stay correct regardless of the invoking CWD.
_here = SPECPATH

datas, binaries, hiddenimports = [], [], []
for pkg in ("torch", "gliner2", "transformers", "tokenizers", "safetensors",
            "huggingface_hub", "spacy", "en_core_web_sm", "wordfreq", "bashlex"):
    d, b, h = collect_all(pkg)
    datas += d
    binaries += b
    hiddenimports += h
hiddenimports += collect_submodules("uvicorn")

# presidio-analyzer (app/pii.py) and its phonenumbers dependency are MODULES ONLY.
#
# Both are invisible to PyInstaller's source analysis for the same reason
# pyarmor_runtime is below: nothing imports them at module scope. app/pii.py
# imports presidio inside its engine builder (deliberately — it keeps the module
# import cheap), presidio resolves its recognizers through its own registry, and
# phonenumbers loads one metadata module per region by name. So none of them
# appears in any import statement PyInstaller can see, and without these two
# lines the FROZEN binary fails at the first /pii while every unit test passes
# — the failure class freeze_support() already cost us once.
#
# collect_submodules, not collect_all: the loop above is for packages that ship
# data files or native libs, and neither of these does in the path we take.
# Verified rather than assumed — instrumenting open() across a real
# app.pii.scan() shows the analyzer reading .py sources and NOTHING else; the
# explicit RecognizerRegistry in pii.py bypasses presidio's conf/*.yaml
# entirely, and phonenumbers' region metadata is Python modules, which is
# exactly what collect_submodules brings.
hiddenimports += collect_submodules("presidio_analyzer")
hiddenimports += collect_submodules("phonenumbers")

# When KELD_OBFUSCATE=1, serve.py + app/* are PyArmor-obfuscated: their imports
# live inside encrypted bytecode that PyInstaller's source analysis can't see, so
# name the app submodules + the pyarmor runtime explicitly (they're discoverable
# on pathex). Without this the frozen app fails with "No module named 'app'".
if os.environ.get("KELD_OBFUSCATE") == "1":
    import glob
    hiddenimports.append("app")
    for f in glob.glob(os.path.join(_here, "app", "**", "*.py"), recursive=True):
        rel = os.path.relpath(f, _here)
        mod = os.path.splitext(rel)[0].replace(os.sep, ".")
        base = os.path.basename(f)
        if base == "__init__.py":
            mod = mod.rsplit(".", 1)[0]
        if not base.startswith("test_"):
            hiddenimports.append(mod)
    for rt in glob.glob(os.path.join(_here, "pyarmor_runtime_*")):
        hiddenimports.append(os.path.basename(rt))

a = Analysis(
    [os.path.join(_here, "serve.py")],
    pathex=[_here],
    datas=datas,
    binaries=binaries,
    hiddenimports=hiddenimports,
    noarchive=False,
)
pyz = PYZ(a.pure)
exe = EXE(
    pyz,
    a.scripts,
    [],
    exclude_binaries=True,
    name="keld-agent-sidecar",
    console=True,
)
coll = COLLECT(exe, a.binaries, a.datas, name="keld-agent-sidecar")
