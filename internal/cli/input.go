package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Prompt assembly, shared by every command that sends text to a model.
//
// Three sources compose, in this order: the positional arguments, files
// named with --file, and piped stdin. The convention is the one every
// unix-shaped LLM tool has converged on — arguments are the instruction,
// the pipe is the material:
//
//	git diff | deepseek chat "write a commit message"
//	deepseek chat "explain" --file main.go --file main_test.go

// readPrompt builds the user message. It returns an error only when there
// is nothing to send at all, which is worth catching before spending a
// request on an empty prompt.
func readPrompt(args []string, files []string, requireText bool) (string, error) {
	var blocks []string

	if instruction := strings.TrimSpace(strings.Join(args, " ")); instruction != "" {
		if instruction == "-" {
			// An explicit "-" means "the prompt is on stdin", even when
			// stdin is a terminal and the caller will type it.
			text, err := io.ReadAll(os.Stdin)
			if err != nil {
				return "", fmt.Errorf("reading stdin: %w", err)
			}
			blocks = append(blocks, strings.TrimRight(string(text), "\n"))
		} else {
			blocks = append(blocks, instruction)
		}
	}

	for _, path := range files {
		block, err := readFileBlock(path)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, block)
	}

	// Piped stdin joins as material. A terminal stdin is left alone: a
	// bare `deepseek chat "hi"` must not hang waiting for EOF.
	if !isTTY(os.Stdin) && !containsDash(args) {
		text, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		if s := strings.TrimRight(string(text), "\n"); s != "" {
			blocks = append(blocks, s)
		}
	}

	prompt := strings.Join(blocks, "\n\n")
	if requireText && strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("nothing to send — pass a prompt, --file, or pipe text in\n  Example: deepseek chat \"why is the sky blue\"")
	}
	return prompt, nil
}

func containsDash(args []string) bool {
	for _, a := range args {
		if a == "-" {
			return true
		}
	}
	return false
}

// readFileBlock reads a file and fences it with its name, so the model is
// told what it is reading rather than handed anonymous text.
func readFileBlock(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading --file %s: %w", path, err)
	}
	name := filepath.Base(path)
	lang := strings.TrimPrefix(filepath.Ext(path), ".")
	return fmt.Sprintf("%s:\n```%s\n%s\n```", name, lang, strings.TrimRight(string(b), "\n")), nil
}

// readMaybeFile resolves a value that may be given inline or as @path.
// Used for system prompts and anything else long enough to keep in a file.
func readMaybeFile(v string) (string, error) {
	if !strings.HasPrefix(v, "@") {
		return v, nil
	}
	path := v[1:]
	if path == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		return strings.TrimRight(string(b), "\n"), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// readJSON resolves a JSON value given inline or as @path, and checks that
// it parses. Tool schemas fail confusingly at the API if they are
// malformed, so they are rejected here instead — with the file name.
func readJSON(v string) (json.RawMessage, error) {
	text, err := readMaybeFile(v)
	if err != nil {
		return nil, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	if !json.Valid([]byte(text)) {
		src := "the value"
		if strings.HasPrefix(v, "@") {
			src = v[1:]
		}
		return nil, fmt.Errorf("%s is not valid JSON", src)
	}
	return json.RawMessage(text), nil
}
