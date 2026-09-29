package pullrequest

import (
	"strings"
	"unicode/utf8"

	"owngit/internal/state"
)

// The rules for the text a pull request carries. The service applies them,
// and the command line and MCP apply the same ones before sending, so every
// entry point refuses a text with the same code, however much encoding the
// request would make it grow.

// CheckText returns input with its title and description as OwnGit keeps
// them, or the problem that refuses them.
func (input CreateInput) CheckText() (CreateInput, error) {
	title, err := pullRequestTitle(input.Title)
	if err != nil {
		return input, err
	}
	body, err := pullRequestText(input.Body, "invalid_body", "description")
	if err != nil {
		return input, err
	}
	input.Title, input.Body = title, body
	return input, nil
}

// CheckText returns input with the title and description it sets as OwnGit
// keeps them, or the problem that refuses them.
func (input EditInput) CheckText() (EditInput, error) {
	if input.Title != nil {
		title, err := pullRequestTitle(*input.Title)
		if err != nil {
			return input, err
		}
		input.Title = &title
	}
	if input.Body != nil {
		body, err := pullRequestText(*input.Body, "invalid_body", "description")
		if err != nil {
			return input, err
		}
		input.Body = &body
	}
	return input, nil
}

// CheckText returns input with its reviewer label and note as OwnGit keeps
// them, or the problem that refuses them.
func (input ReviewSubmitInput) CheckText() (ReviewSubmitInput, error) {
	reviewer := strings.TrimSpace(input.ReviewerLabel)
	if !validLabel(reviewer, 200) {
		return input, NewProblem("invalid_reviewer_label", "The supplied reviewer label must be one line of 1 to 200 bytes of UTF-8 text: up to 200 characters in English, or about 66 in Korean.")
	}
	note, err := pullRequestText(input.Note, "invalid_note", "review note")
	if err != nil {
		return input, err
	}
	input.ReviewerLabel, input.Note = reviewer, note
	return input, nil
}

// pullRequestTitle is a title without surrounding spaces, or invalid_title.
func pullRequestTitle(value string) (string, error) {
	title := strings.TrimSpace(value)
	if !validLabel(title, 500) {
		return "", NewProblem("invalid_title", "The pull request title must be one line of 1 to 500 bytes of UTF-8 text: up to 500 characters in English, or about 160 in Korean.")
	}
	return title, nil
}

// pullRequestText is a description or review note as OwnGit keeps it: line
// ends are LF, so a text saved from a browser or a Windows file does not
// differ from the same text saved elsewhere. It is refused with code when it
// is longer than 64 KiB, is not UTF-8, or holds a NUL character.
func pullRequestText(value, code, name string) (string, error) {
	text := strings.ReplaceAll(value, "\r\n", "\n")
	if !state.ValidPullRequestText(text) {
		return "", NewProblem(code, "The "+name+" must be UTF-8 text of at most 64 KiB (65,536 bytes) without NUL characters.")
	}
	return text, nil
}

// validLabel reports whether value is one line of 1 to maximum bytes of
// UTF-8. The limit counts bytes, as the stored record and its backup do, so
// every message about it says what that means for Korean text.
func validLabel(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}
