"""The attribution verifier: a small local LLM giving YES/NO on borderline
(block, project) pairs. Gemma 4 E2B Q4_K_M via llama-cpp-python, CPU only.

OFF by default, even within the attribution gate; KELD_ATTRIBUTION_VERIFIER=1
opts a machine in. The caller states `opted_out` in the answer's meta, never
silently narrows. The model is lazy: importing this module loads nothing; a
Verifier() loads once.

⚠️ Default-ON until 2026-09-03. Flipped because the one real-data A/B went
1-for-3 for minutes of CPU per block and ~3 GB of weights beside the encoder's
1.7 GB, and because every quality figure in the attribution note's §8 was
measured without it — so the default now matches what was measured. `enabled()`
is mirrored EXACTLY by `attrib.VerifierEnabled()` on the Go side, which also
gates the GGUF download; change both or a daemon fetches weights the sidecar
never loads (or the reverse, which reports `unavailable` forever)."""
import os
import time

MAX_BLOCK_CHARS = 2500

VERIFY_PROMPT = """You are classifying whether a block of work relates to a specific company project.

PROJECT: {title} (team: {team})
{description}
Keywords: {keywords}

WORK CONTEXT: repo={repo} branch={branch}

WORK (may be truncated):
{text}

Question: Is this work part of the project "{title}"? Work on the same general topic but for personal use or a different initiative does NOT count. Answer with exactly one word: YES or NO."""


def enabled():
    return os.environ.get("KELD_ATTRIBUTION_VERIFIER", "").strip().lower() \
        in ("1", "true", "on", "yes")


def weights_path():
    explicit = os.environ.get("KELD_VERIFIER_GGUF")
    if explicit:
        return explicit if os.path.isfile(explicit) else None
    home = os.environ.get("KELD_HOME") or os.path.join(os.path.expanduser("~"), ".keld")
    p = os.path.join(home, "models", "gemma-4-e2b", "model.gguf")
    return p if os.path.isfile(p) else None


class Verifier:
    def __init__(self, path=None, n_threads=None):
        from llama_cpp import Llama  # deliberate: import at load, not at module import
        self._llm = Llama(model_path=path or weights_path(), n_ctx=4096,
                          n_gpu_layers=0, n_threads=n_threads or max(2, os.cpu_count() // 2),
                          verbose=False)

    def warm(self):
        """One throwaway single-token generation, so the child signals ready only once the
        model can actually produce a token.

        ⚠️ `worker.serve` prefers a model's own `warm()` and falls back to a GLiNER2-shaped
        `classify_text` call inside a bare `except`. This class had no `warm()`, so it took
        that fallback, the AttributeError was swallowed, and the child answered
        `{"ready": True}` having done nothing beyond `Llama(...)` — a contract
        `test_verifier_worker.py` pinned against a fake that no production class implemented.
        Constructing a Llama is not the same as it working: `create_chat_completion` is what
        allocates the KV cache and first touches the mmap'd weights, so without this the
        first REAL verdict pays that cost inside a caller's budget and any failure in it
        surfaces as a mid-request error rather than a failed spawn. It is also what makes
        `WorkerManager._spawn`'s post-ready `model_cost_mb` measurement mean something: taken
        before any generation, it measures a process that has barely mapped the file.

        Deliberately max_tokens=1 and a fixed trivial prompt — the cheapest work that is
        still real. Failures propagate to serve()'s own try/except, which already treats a
        failed warm-up as non-fatal.
        """
        self._llm.create_chat_completion(
            messages=[{"role": "user", "content": "Answer with exactly one word: YES"}],
            max_tokens=1, temperature=0)

    def verify(self, block_text, dims, project):
        d = dims or {}
        prompt = VERIFY_PROMPT.format(
            title=project.get("title", ""), team=project.get("team", ""),
            description=project.get("description", ""),
            keywords=", ".join(project.get("keywords") or []),
            repo=d.get("repo", "?"), branch=d.get("branch", "?"),
            text=block_text[:MAX_BLOCK_CHARS])
        t0 = time.time()
        out = self._llm.create_chat_completion(
            messages=[{"role": "user", "content": prompt}],
            max_tokens=8, temperature=0)
        text = out["choices"][0]["message"]["content"].strip().upper()
        return text.startswith("YES") or "YES" in text[:12], time.time() - t0
