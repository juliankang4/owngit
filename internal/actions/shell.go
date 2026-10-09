package actions

import (
	"fmt"
	"strings"
)

// ShellWords splits an interpreter template without shell expansion.
func ShellWords(text string) ([]string, error) {
	var words []string
	var word strings.Builder
	quote := rune(0)
	started := false
	for _, char := range text {
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				word.WriteRune(char)
			}
			continue
		}
		switch char {
		case '\'', '"':
			quote, started = char, true
		case ' ', '\t', '\r', '\n':
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
		default:
			started = true
			word.WriteRune(char)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("workflow.shell: unterminated custom shell quote")
	}
	if started {
		words = append(words, word.String())
	}
	if len(words) == 0 || words[0] == "" {
		return nil, fmt.Errorf("workflow.shell: empty custom shell")
	}
	return words, nil
}
