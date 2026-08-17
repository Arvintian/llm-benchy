// Package corpus downloads a text book and tokenizes it for use as
// benchmark prompt material.
package corpus

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/hupe1980/go-tiktoken"
)

// Tokenizer tokenizes and detokenizes text.
type Tokenizer interface {
	Encode(text string) []int32
	Decode(tokens []int32) string
}

// TokenizedCorpus holds the tokenized book text used to build prompts.
type TokenizedCorpus struct {
	BookURL   string
	Tokenizer Tokenizer
	Tokens    []int32
}

// NewTokenizedCorpus creates a corpus from the given book URL.
//
// Note: the Python original uses the HuggingFace transformers tokenizer
// matching the model under test. The Go port uses the GPT-2 byte-level BPE
// tokenizer (the same fallback the Python version uses when the model
// tokenizer cannot be loaded).
func NewTokenizedCorpus(bookURL, tokenizerName, modelName string) *TokenizedCorpus {
	name := tokenizerName
	if name == "" {
		name = modelName
	}

	var tok Tokenizer
	t, err := tiktoken.NewEncodingByName("gpt2")
	if err != nil {
		fmt.Printf("Error loading tokenizer: %v\n", err)
	} else {
		tok = &gpt2Tokenizer{enc: t}
	}
	if tok == nil {
		fmt.Println("Falling back to whitespace tokenizer as approximation.")
		tok = &whitespaceTokenizer{}
	}
	if name != "" && name != "gpt2" {
		fmt.Printf("Note: using gpt2 BPE tokenizer as approximation for '%s'.\n", name)
	}

	c := &TokenizedCorpus{BookURL: bookURL, Tokenizer: tok}
	c.Tokens = c.loadData()
	return c
}

type gpt2Tokenizer struct {
	enc *tiktoken.Encoding
}

func (g *gpt2Tokenizer) Encode(text string) []int32 {
	ids, _ := g.enc.EncodeOrdinary(text)
	out := make([]int32, len(ids))
	for i, id := range ids {
		out[i] = int32(id)
	}
	return out
}

func (g *gpt2Tokenizer) Decode(tokens []int32) string {
	ids := make([]uint, len(tokens))
	for i, t := range tokens {
		ids[i] = uint(t)
	}
	return string(g.enc.Decode(ids))
}

// whitespaceTokenizer is a minimal fallback tokenizer that splits text
// on whitespace (1 token per word).
type whitespaceTokenizer struct{}

func (w *whitespaceTokenizer) Encode(text string) []int32 {
	fields := strings.Fields(text)
	tokens := make([]int32, 0, len(fields))
	for i := range fields {
		tokens = append(tokens, int32(i+1))
	}
	return tokens
}

func (w *whitespaceTokenizer) Decode(tokens []int32) string {
	words := make([]string, len(tokens))
	for i := range tokens {
		words[i] = "word"
	}
	return strings.Join(words, " ")
}

func (c *TokenizedCorpus) loadData() []int32 {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	cacheDir := filepath.Join(home, ".cache", "llama-benchy")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		fmt.Printf("Error creating cache directory: %v\n", err)
		os.Exit(1)
	}

	sum := md5.Sum([]byte(c.BookURL))
	cacheFile := filepath.Join(cacheDir, hex.EncodeToString(sum[:])+".txt")

	var text string
	if _, err := os.Stat(cacheFile); err == nil {
		fmt.Printf("Loading text from cache: %s\n", cacheFile)
		data, err := os.ReadFile(cacheFile)
		if err != nil {
			fmt.Printf("Error reading cache: %v\n", err)
			os.Exit(1)
		}
		text = string(data)
	} else {
		fmt.Printf("Downloading book from %s...\n", c.BookURL)
		resp, err := http.Get(c.BookURL)
		if err != nil {
			fmt.Printf("Error downloading or processing book: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			fmt.Printf("Error downloading or processing book: HTTP %d\n", resp.StatusCode)
			os.Exit(1)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Printf("Error downloading or processing book: %v\n", err)
			os.Exit(1)
		}
		text = string(data)
		// Basic cleanup
		if idx := strings.Index(text, "*** START OF THE PROJECT GUTENBERG EBOOK"); idx != -1 {
			text = text[idx:]
		}
		if err := os.WriteFile(cacheFile, []byte(text), 0o644); err != nil {
			fmt.Printf("Error saving cache: %v\n", err)
		} else {
			fmt.Printf("Saved text to cache: %s\n", cacheFile)
		}
	}

	return c.Tokenizer.Encode(text)
}

// Len returns the number of tokens in the corpus.
func (c *TokenizedCorpus) Len() int { return len(c.Tokens) }

// GetTokenizer returns the underlying tokenizer.
func (c *TokenizedCorpus) GetTokenizer() Tokenizer { return c.Tokenizer }

// GetTokens returns the raw token list.
func (c *TokenizedCorpus) GetTokens() []int32 { return c.Tokens }
