# Task 3 report: the arms and the bar

Status: DONE_WITH_CONCERNS. Commits: d9b9c439 (arms + bar), 0049a078 (corrected to the controller's rulings).

## What exists
`scripts/convdomain_study.py`: keyword_arm, gliner_raw/gliner_arm, union_arm, shuffled_arm (seed 20261001, in source), BAR_PRECISION=0.80, BAR_ACCURACY_MARGIN=0.20, LABELLER_WINDOW=4000 (mirrors convdomain_render.py), `predict` CLI.
Run `~/.keld/sidecar-venv/bin/python scripts/convdomain_study.py` for the self-test (passes) or `... predict` to write predictions.

## Rulings applied
- Arms read ONLY `text` of /tmp/claude-1000/convdomain/frame.ndjson. Not id, stratum, hit_domains, chars, turns. Predictions are emitted in frame line order (join by position).
- Every arm scores exactly text[:4000], a raw cut, no turn adjustment (the labeller's view). The study therefore measures HEAD-OF-CONVERSATION recognition, narrower than whole-conversation.
- Abstention = None; the scorer (Task 4) maps it to "none". `other` is not in any vocabulary. The scorer must report both accuracies and precision `n/a` when an arm answers nothing. The majority-class floor needs truth, so it is the scorer's, not an arm's.
- shuffled arm = the union arm's predictions permuted (keeps marginal and abstention rate, destroys alignment); it needs no truth.
- Word lists were written once from the brief and not iterated. The Step 2 self-tests are the only tuning.

## Outputs (arm predictions only, no scoring, labels never read)
/tmp/claude-1000/convdomain/arm_predictions.json (keys keyword, gliner, union, shuffled, in frame order), gliner_raw.json (label + confidence cache, keyed by text sha1; GLiNER ran once, on GPU).
Prediction counts: keyword abstains on 48/120 (marketing 27, engineering 13, hr 10, sales 10, medical 8, financial 4, legal 0). GLiNER never abstains: it is a forced 7-way choice with no "none" option. Union therefore never abstains either.

## Concerns
1. The controller's described `scripts/convdomain-sample.jsonl` did not exist; the frame is the /tmp ndjson, as corrected mid-task. The first GLiNER attempt died with CUDA OOM on the full text. After the 4000-char window it ran fine.
2. GLiNER and union cannot say "none", and ~50 truth rows are `none` (count known only from the commit message). Their accuracy will be capped by construction. This is faithful to the ruling, but Task 5 should state it. Adding a none-threshold on confidence would be a new arm needing its own pre-registration; I did not add one.
3. The keyword arm's lists contain `legal` hits 0 times on this sample, and its `ad`, `copy`, `content`, `channel` words are generic. Both are findings, not tuned.
4. I did not look at the labels file or its history, and did not score anything.

## Fix round 1
Null label (exact): "a hobby, school or everyday personal conversation, not anyone's professional work". It competes in the same ranking as the seven (the NULL_DOC idiom); if it wins, the arm predicts `none`. No threshold. gliner_8 inference is cached separately in gliner8_raw.json; gliner_raw.json is untouched.

Arms now (predictions in arm_predictions.json, frame order; a None entry is scored "none"):
- keyword: 7 domains + abstain. Cannot say `other`.
- gliner_7: 7 domains only, cannot say none (kept as the vocabulary-effect contrast).
- gliner_8: 7 domains + none (null label won).
- union: keyword, else gliner_8.
- shuffled_of_union: the union's predictions permuted, seed 20261001. Nothing can say `other`.

Keyword-list changes (blind, once, no iteration):
- Legal: added indemnity, tenancy, non, disclosure, terms, conditions, service, privacy, policy, plaintiff, defendant, lawsuit, litigation, attorney, solicitor, lawyer, statute, trademark, copyright, infringement, power, affidavit, settlement, arbitration, court, legislation. Note the list is bag-of-words, so "non disclosure" and "power of attorney" are matched as separate words.
- Root cause of the zero legal predictions, found on inspection: matching was whole-word with no plurals ("contracts", "clauses" never hit). I added plural-tolerant matching (+s, +es) for all lists. This is a construction fix, not tuning.
- Small additions to the other six lists: marketing (marketing, social, media, promotion, slogan, tagline), financial (accounting, profit, expenses, cashflow, tax, taxes, investment), sales (sales, salesperson, customers), medical (doctor, medication, medical, clinic, hospital), hr (job, vacancy, salary, recruitment, hr), engineering (css, html, sql, react, bash, software, programming).
- ORDERING CAVEAT: the legal terms were added after observing a zero legal-prediction count and before any accuracy was known. After the change keyword predicts legal 20 times (a count, not an accuracy). The other-list additions were made at the same time, from first principles, with no counts consulted.

Counts after the fix (predictions only): keyword abstains on 40/120; gliner_8 predicts none on 55/120; union none on 21/120.

## Fix round 2
Parser fix, no vocabulary change: terms joined with "_" in the KW strings are phrases, matched contiguously after the stopwords of/and/the/a are removed from the text (so "terms_service" matches "terms of service"). Plural matching applies to the phrase's last word. Phrases: subject_line, open_rate, landing_page, content_calendar, social_media, cold_email, offer_letter, cover_letter, performance_review, employee_handbook, non_disclosure, terms_conditions, terms_service, privacy_policy, power_attorney, copyright_infringement. No word was added or dropped; the only consequence is that "terms", "service", "conditions", "power" and the like no longer fire alone ("attorney" stays a term of its own). The unterminated-fence fix: strip_code now treats an unclosed ``` as running to the end of the window. The self-test pins "in terms of cost" != legal, "terms of service" == legal, "power of attorney" == legal, and an unclosed fence scoring nothing. GLiNER caches were not regenerated.

Counts after the fix (predictions only):
- keyword: None 49, engineering 25, marketing 13, medical 11, sales 8, financial 8, legal 4, hr 2.
- gliner_7: unchanged. gliner_8: unchanged.
- union: engineering 32, None 28, medical 18, marketing 14, financial 11, sales 9, legal 5, hr 3.
- shuffled_of_union: same counts as union, by construction.

Disclosure of aggregate information consulted. (a) The controller's dispatch stated that 72 of 120 truths are none/other and the majority baseline is 41.7%. (b) In my first command I ran `git show --stat HEAD`, which printed the Task 2 commit message: none 50, engineering 25, other 22, marketing 8, financial 6, medical 6, legal 1, sales 1, hr 1; modes doing 83, asking 37. That is the aggregate distribution and I saw it before writing any arm. I never opened the labels file or its history, saw no per-row label or accuracy, and did not score. I did not use the distribution to choose vocabulary, but I cannot rule out that it influenced me: the legal under-prediction I noticed and fixed in round 1 was against a truth known to have almost no legal rows, so it was not motivated by it, but a reader should know both were visible.
