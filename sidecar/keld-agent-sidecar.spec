# PyInstaller spec — build with: pyinstaller sidecar/keld-agent-sidecar.spec
# One-dir keeps torch's shared libs + data files intact (one-file unpacks slowly
# and is fragile with torch). Produces dist/keld-agent-sidecar/keld-agent-sidecar.
import os

from PyInstaller.utils.hooks import collect_all, collect_submodules

# PyInstaller resolves relative paths in a spec against the spec's own directory,
# so anchor to SPECPATH (…/sidecar) to stay correct regardless of the invoking CWD.
_here = SPECPATH

datas, binaries, hiddenimports = [], [], []
for pkg in ("torch", "gliner2", "transformers", "tokenizers", "safetensors", "huggingface_hub"):
    d, b, h = collect_all(pkg)
    datas += d
    binaries += b
    hiddenimports += h
hiddenimports += collect_submodules("uvicorn")

# When KELD_OBFUSCATE=1, serve.py + app/* are PyArmor-obfuscated: their imports
# live inside encrypted bytecode that PyInstaller's source analysis can't see, so
# name the app submodules + the pyarmor runtime explicitly (they're discoverable
# on pathex). Without this the frozen app fails with "No module named 'app'".
if os.environ.get("KELD_OBFUSCATE") == "1":
    import glob
    hiddenimports.append("app")
    for f in glob.glob(os.path.join(_here, "app", "*.py")):
        mod = os.path.splitext(os.path.basename(f))[0]
        if mod != "__init__" and not mod.startswith("test_"):
            hiddenimports.append("app." + mod)
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
