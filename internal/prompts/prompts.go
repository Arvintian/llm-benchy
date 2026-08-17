// Package prompts generates benchmark prompts from a tokenized corpus.
package prompts

import (
	"crypto/rand"
	"fmt"
	mathrand "math/rand"

	"llm-benchy/internal/corpus"
)

// PromptGenerator builds (context, prompt) pairs from a corpus.
type PromptGenerator struct {
	Corpus    *corpus.TokenizedCorpus
	Tokenizer corpus.Tokenizer
	AllTokens []int32
}

// NewPromptGenerator creates a generator over the given corpus.
func NewPromptGenerator(c *corpus.TokenizedCorpus) *PromptGenerator {
	return &PromptGenerator{
		Corpus:    c,
		Tokenizer: c.GetTokenizer(),
		AllTokens: c.GetTokens(),
	}
}

func newUUID4() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Extremely unlikely; fall back to zeros.
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Generate generates a single (context, prompt) pair.
func (g *PromptGenerator) Generate(promptTokens int, contextTokens int, noCache bool) (string, string) {
	suffix := ""
	suffixLen := 0
	if noCache {
		suffix = " " + newUUID4()
		suffixLen = len(g.Tokenizer.Encode(suffix))
	}

	// Adjust prompt tokens to fetch from text
	textPromptTokens := promptTokens - suffixLen
	if textPromptTokens < 0 {
		textPromptTokens = 0
	}

	totalNeeded := textPromptTokens + contextTokens
	if totalNeeded <= 0 {
		return "", suffix
	}

	// Extend tokens if not enough
	currentTokens := g.AllTokens
	if len(currentTokens) < totalNeeded {
		repeats := totalNeeded/len(currentTokens) + 2
		extended := make([]int32, 0, len(currentTokens)*repeats)
		for i := 0; i < repeats; i++ {
			extended = append(extended, currentTokens...)
		}
		currentTokens = extended
	}

	// Pick a random start position
	maxStart := len(currentTokens) - totalNeeded
	if maxStart < 1 {
		maxStart = 1
	}
	startIdx := mathrand.Intn(maxStart)

	selected := currentTokens[startIdx : startIdx+totalNeeded]

	var contextText, promptText string
	if contextTokens > 0 {
		contextText = g.Tokenizer.Decode(selected[:contextTokens])
	}
	promptText = g.Tokenizer.Decode(selected[contextTokens:])

	if noCache {
		promptText += suffix
	}

	return contextText, promptText
}

// GenerateBatch generates a batch of (context, prompt) pairs.
func (g *PromptGenerator) GenerateBatch(batchSize, promptTokens, contextTokens int, noCache bool) [][2]string {
	batch := make([][2]string, batchSize)
	for i := 0; i < batchSize; i++ {
		c, p := g.Generate(promptTokens, contextTokens, noCache)
		batch[i] = [2]string{c, p}
	}
	return batch
}
