package invariants

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// INV-NO-LLM (fitness, level: architecture) — DESIGN §2.
//
// wi is a deterministic primitive: it contains NO LLM. This guard walks the
// module graph (go.mod + go.sum — go.sum lists the full transitive closure) and
// fails if any known LLM / agent-SDK token appears in a module path. It is a
// belt: even a present-but-unused LLM module trips it.
//
// Non-vacuity (guard→mutant): emptying llmDenylist or breaking scanForDenylisted
// (so it never matches) turns TestNoLLMScannerIsNonVacuous RED. The real-source
// mutant is "add a denylisted module to go.mod/go.sum" → TestNoLLMDependencies
// RED; the non-vacuity test exercises the identical detector on a synthetic
// corpus so a green suite cannot be a broken-detector false negative.

// llmDenylist is a curated set of lowercase tokens that appear in LLM / agent
// SDK module paths. It is data, not logic — extend it as the ecosystem grows.
// Tokens are matched as case-insensitive substrings of module paths.
var llmDenylist = []string{
	"anthropic",
	"openai",
	"go-openai",
	"langchain",
	"cohere",
	"huggingface",
	"/genai",
	"generative-ai",
	"ollama",
	"replicate-go",
	"vertexai",
	"bedrockruntime",
}

// scanForDenylisted returns every denylist token found (case-insensitively) in
// content. A pure function so the non-vacuity test can exercise it directly.
func scanForDenylisted(content string, denylist []string) []string {
	lc := strings.ToLower(content)
	var hits []string
	for _, bad := range denylist {
		if strings.Contains(lc, bad) {
			hits = append(hits, bad)
		}
	}
	return hits
}

// moduleRoot walks up from the test's working directory to the directory holding
// go.mod, so the guard works regardless of where in the tree the package sits.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from test cwd")
		}
		dir = parent
	}
}

func TestNoLLMDependencies(t *testing.T) {
	root := moduleRoot(t)
	for _, name := range []string{"go.mod", "go.sum"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			if name == "go.sum" && os.IsNotExist(err) {
				continue // go.sum may be absent when there are no deps
			}
			t.Fatalf("read %s: %v", name, err)
		}
		if hits := scanForDenylisted(string(b), llmDenylist); len(hits) > 0 {
			t.Errorf("%s references denylisted LLM/agent SDK token(s) %v — wi must contain NO LLM (DESIGN §2)", name, hits)
		}
	}
}

func TestNoLLMScannerIsNonVacuous(t *testing.T) {
	// A line as it would appear in go.sum if an LLM SDK were ever pulled in.
	corpus := "github.com/sashabaranov/go-openai v1.17.0 h1:deadbeef=\n"
	if hits := scanForDenylisted(corpus, llmDenylist); len(hits) == 0 {
		t.Fatal("INV-NO-LLM scanner is vacuous: it failed to flag a known LLM SDK module path")
	}
}
