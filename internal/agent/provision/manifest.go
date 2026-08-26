package provision

const (
	ModelRepo     = "fastino/gliner2-large-v1"
	ModelRevision = "b122b11eeaee4dabd32bed80412f3234c0d0e943"
	// ModelSHA256 is the sha256 of model.safetensors (the sentinel EnsureModel verifies).
	ModelSHA256 = "92a76e84cd4de59e15e3f6577bef9e4304929667551ee053665eba365510638e"
)

// THE TEXT ENCODER — the sidecar's textembed child, gated on KELD_TEXTEMBED.
//
// A second repo pinned the same way as the first, and the pin is not decoration:
// the published vector is a 256-d MRL prefix slice of this model's output, so a
// corpus collected against one revision cannot be compared with one collected
// against another. Revision-pinning is what makes "the same vector on every
// machine in the fleet" true rather than hoped for.
//
// ⚠️ VERIFIED AGAINST THE REAL REPO, not assembled from memory. The revision's
// siblings manifest at the pinned sha is exactly twelve entries, and the ten
// that survive hf.go's nonModelFile denylist are byte-for-byte the ten in the
// known-good local directory a hand-fetch produced:
//
//	config.json (727)                       transformers.AutoModel
//	model.safetensors (1,191,586,416)       transformers.AutoModel — the sentinel
//	generation_config.json (117)            AutoModel, present-if-found
//	tokenizer.json (11,423,705)             AutoTokenizer
//	tokenizer_config.json (9,706)           AutoTokenizer
//	vocab.json (2,776,833)                  AutoTokenizer (BPE)
//	merges.txt (1,671,853)                  AutoTokenizer (BPE) — and the reason
//	                                        nonModelFile does not deny ".txt"
//	modules.json (349)                      sentence-transformers furniture
//	config_sentence_transformers.json (215) sentence-transformers furniture
//	1_Pooling/config.json (313)             sentence-transformers furniture
//
// The last three are kilobytes and are NOT opened by this sidecar (it pools
// last-token itself via AutoModel, never SentenceTransformer), but they are
// kept for the reason nonModelFile exists: the denylist judges shape, not a
// list of files someone believed the loader wanted, and a missing config is a
// runtime load failure no unit test can catch. Denied: README.md (17 KB) and
// .gitattributes — the same two shapes the GLiNER2 repo ships.
const (
	EncoderRepo     = "Qwen/Qwen3-Embedding-0.6B"
	EncoderRevision = "97b0c614be4d77ee51c0cef4e5f07c00f9eb65b3"
	// EncoderSHA256 is the sha256 of model.safetensors at that revision —
	// confirmed two ways, a local sha256sum of the known-good directory and
	// Hugging Face's own X-Linked-ETag for the resolve URL, which agree.
	EncoderSHA256 = "0437e45c94563b09e13cb7a64478fc406947a93cb34a7e05870fc8dcd48e23fd"
	// EncoderDirName is the directory under ~/.keld/models the weights land in.
	// It MUST match textembed.weights_dir()'s default, which is what lets a
	// sidecar already running when the fetch finishes pick the weights up
	// without a respawn or a KELD_TEXTEMBED_DIR to point it there.
	EncoderDirName = "qwen3-embedding-0.6b"
)
