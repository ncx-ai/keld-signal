# Task 4 report
Status: done. Labels in scripts/verbsplit-labels.txt, all 200 frame ids once each, vocabulary valid.
unclear: 138 of 200.
Rendered with id, tool names, tool args, out, text (first 1500 chars) only; corpus/cls/prior withheld.
Concerns: most rows are not decidable. Image Reads (screenshots) and empty-text tool calls carry no
verb evidence; plain generation rows (stories, essays, advice, code) are neither summarize/research/extract.
Narrow Reads (offset/limit), sed ranges and targeted greps were labelled extract; whole-file Reads, multi-file
greps, web search, notion-fetch were research; status/wrap-up reports condensing in-conversation work were
text.summarize. Those boundaries are judgement calls, esp. grep (extract vs research) and git/ls state checks (unclear).
Text was truncated at 1500 chars in rendering, so long outputs were judged from their opening.
