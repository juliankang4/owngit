package repository

import (
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// RefNameKey returns the key under which OwnGit treats two ref names as the
// same name. Git stores a loose ref as a file named after the ref, and file
// systems that ignore letter case or Unicode form (APFS and HFS+ on macOS,
// NTFS on Windows, case-insensitive SMB shares) open one file for names
// that differ only in those ways. The key makes every such pair equal:
//
//  1. canonical decomposition (NFD), so composed and decomposed spellings,
//     such as "café" with U+00E9 or with "e" and U+0301 and Hangul
//     syllables or conjoining jamo, match as on APFS and HFS+;
//  2. Unicode full case folding, so "ß" matches "ss" and "ſ" matches "s";
//  3. dropping format characters (category Cf, such as U+200D), which HFS+
//     ignores in names;
//  4. mapping each character to upper case and back to lower case, so
//     characters that NTFS's upper-case table makes equal match, such as
//     the dotless "ı" and "i";
//  5. NFD again, because steps 2 and 4 can produce composed characters.
//
// It matches some names that a given file system keeps apart, which only
// refuses more pushes; the rule is the same on every system, so a
// repository behaves the same wherever it is stored or restored.
func RefNameKey(name string) string {
	folded := cases.Fold().String(norm.NFD.String(name))
	var key strings.Builder
	key.Grow(len(folded))
	for _, character := range folded {
		if unicode.Is(unicode.Cf, character) {
			continue
		}
		key.WriteRune(unicode.ToLower(unicode.ToUpper(character)))
	}
	return norm.NFD.String(key.String())
}

// RefNameConflicts returns the names in writes that share their RefNameKey
// with a different name in existing or in writes. A push or an import
// creates, changes and deletes none of them: where such names are one file,
// writing one would change or remove the other.
func RefNameConflicts(existing, writes []string) map[string]bool {
	spellings := make(map[string]map[string]bool, len(existing)+len(writes))
	for _, names := range [][]string{existing, writes} {
		for _, name := range names {
			key := RefNameKey(name)
			if spellings[key] == nil {
				spellings[key] = map[string]bool{}
			}
			spellings[key][name] = true
		}
	}
	conflicts := map[string]bool{}
	for _, name := range writes {
		if len(spellings[RefNameKey(name)]) > 1 {
			conflicts[name] = true
		}
	}
	return conflicts
}
