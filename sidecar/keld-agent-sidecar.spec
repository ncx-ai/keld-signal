# PyInstaller spec — build with: pyinstaller sidecar/keld-agent-sidecar.spec
# One-dir keeps torch's shared libs + data files intact (one-file unpacks slowly
# and is fragile with torch). Produces dist/keld-agent-sidecar/keld-agent-sidecar.
from PyInstaller.utils.hooks import collect_all, collect_submodules

datas, binaries, hiddenimports = [], [], []
for pkg in ("torch", "gliner2", "transformers", "tokenizers", "safetensors", "huggingface_hub"):
    d, b, h = collect_all(pkg)
    datas += d
    binaries += b
    hiddenimports += h
hiddenimports += collect_submodules("uvicorn")

a = Analysis(
    ["sidecar/serve.py"],
    pathex=["sidecar"],
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
