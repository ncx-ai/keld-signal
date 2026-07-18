# Research notes — credential/secret leak detection (deterministic + semantic)

**Date:** 2026-07-18. Deep-research pass (5 angles, 21 sources fetched, 103 claims
extracted, 25 adversarially verified → 23 confirmed / 2 killed, 10 synthesized).
Informs `docs/superpowers/specs/2026-07-18-credential-leak-detection-design.md`.

## Verified findings (high confidence unless noted)

1. **Known credentials → exact prefix+length regex + keyword prequalifier, not
   entropy.** Targets: AWS `(A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA|AGPA|AIDA|AROA|AIPA|
   ANPA|ANVA)[A-Z0-9]{16}`; GitHub `ghp_/gho_/ghu_/ghs_/ghr_`+36, `github_pat_`+82;
   Slack `xox[baprs]-…`; Google `AIza[0-9A-Za-z\-_]{35}`; Stripe `(sk|rk)_live_
   [0-9a-zA-Z]{24}`; Square `sq0atp-/sq0csp-`. gitleaks ships these (Go+RE2).
2. **Generic high-entropy detection must be keyword-gated with a higher floor.**
   gitleaks generic-api-key requires a proximity keyword (access|auth|key|password|
   secret|token) near an assignment, then base64-like capture at entropy 3.5.
3. **detect-secrets entropy defaults: base64 ≥4.5, hex ≥3.0**, with an integer
   penalty (`-1.2/log2(len)`) to suppress numeric strings.
4. **Entropy alone can't separate secrets from high-entropy non-secrets.**
   TruffleHog scored a secret 4.08 vs the phrase "ThisIsAReallyLongString" 4.11
   (non-secret higher); git SHAs emitted as secrets. (arXiv 2307.00714)
5. **Production scanners layer regex + verification + entropy filtering.** TruffleHog:
   800+ types, identity mapping, live verification (e.g. AWS GetCallerIdentity),
   `--filter-entropy` (start 3.0). (Verification is out of scope for us — would
   exfiltrate the secret.)
6. **No single scanner catches all; tools surface non-overlapping TPs** (gitleaks
   1,533 unique, TruffleHog 438 unique) → union/defense-in-depth. Benchmark recall:
   gitleaks 88%, SpectralOps 67%, TruffleHog 52%; GH precision lead 75%. (2307.00714)
7. **SecretBench** (MSR 2023): 15,084 labeled secrets — Private Key 5,789 / API Key
   4,529 / Auth Token 3,569 / Other 524 / Generic 334 / DB URL 162 / Password 150 /
   Username 27. Defines coverage + ground truth. Password/Username (smallest) = the
   format-free contextual cases where regex fails and ML earns its keep.
8. **ML/semantic adds recall exactly where regex+entropy fail** — contextual/prose
   secrets, novel formats. GLiNER = zero-shot NER, arbitrary label list at
   inference ('password','api key'), semantic-context matching. (gliner-pii-edge
   exposes 'password'/'username' entity types.)
9. **ML's second job = precision-gating** placeholders (`YOUR_API_KEY`), redacted
   (`sk_**`), dummies. Fine-tuned CodeBERT 92.9%R/92.5%P, Qwen-7B 94.1/94.8 vs
   regex 6.8%P@100%R. (arXiv 2410.23657)
10. **Division of labor** (medium confidence — precedence is our design choice):
    L1 regex always-fires; L2 entropy context-gated only; L3 GLiNER independent
    over raw text (recall) + precision gate. Union of spans. Measure vs SecretBench.

## Killed / cautioned
- **2 claims killed** in verification (2/3 refute).
- **Domain-transfer caveat:** every quantitative benchmark measures secrets in
  SOURCE CODE / GitHub issues, NOT user-typed LLM prompts. Structural conclusions
  transfer (regex-for-known, entropy-needs-context, ML-for-contextual); exact
  recall/precision numbers will NOT. Wiz's 82%/85.7% (and ~60% regex baseline) is a
  vendor self-report on a private test set — cite as such.
- **Critical bound:** arXiv 2410.23657's 92–94% ML numbers measure ML as a
  downstream classifier over regex candidates — they do NOT prove ML adds
  novel-format recall as a standalone detector. Hence L3 MUST run independently
  over raw text, and we must measure that config ourselves (open question below).

## Open questions (→ the empirical probe answers these)
1. GLiNER recall/FPR as an *independent* detector on freeform LLM prompts (no cited
   source measures this exact config on this input domain).
2. On-device latency/model-size budget for the GLiNER pass on every prompt; does an
   edge PII variant retain contextual-password recall?
3. Span-precedence policy when L1 matches a format but L3 calls it placeholder.

## Key sources (primary)
- gitleaks config (rules): https://github.com/gitleaks/gitleaks/blob/master/config/gitleaks.toml
- detect-secrets entropy: https://github.com/Yelp/detect-secrets/blob/master/detect_secrets/plugins/high_entropy_strings.py
- TruffleHog: https://github.com/trufflesecurity/trufflehog
- Scanner benchmark (entropy limits, tool overlap): https://arxiv.org/html/2307.00714
- SecretBench dataset: https://www.researchgate.net/publication/369184614_SecretBench_A_Dataset_of_Software_Secrets
- ML precision-gating / regex-ceiling bound: https://arxiv.org/html/2410.23657v4
- GLiNER + Presidio: https://microsoft.github.io/presidio/samples/python/gliner/
- Wiz small-LM (vendor self-report): https://www.wiz.io/blog/small-language-model-for-secrets-detection-in-code
