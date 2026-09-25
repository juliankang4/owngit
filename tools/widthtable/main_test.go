package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseMergesMatchingRanges(t *testing.T) {
	input := `# EastAsianWidth-16.0.0.txt
# @missing: 0000..10FFFF; N
0020           ; Na # Zs         SPACE
1100..115F     ; W  # Lo    [96] HANGUL CHOSEONG KIYEOK..HANGUL CHOSEONG FILLER
1160..11FF     ; N  # Lo   [160] HANGUL JUNGSEONG FILLER..HANGUL JONGSEONG SSANGNIEUN
3000           ; F  # Zs         IDEOGRAPHIC SPACE
3001..3003     ; W  # Po     [3] IDEOGRAPHIC COMMA..DITTO MARK
AC00..D7A3     ; W  # Lo [11172] HANGUL SYLLABLE GA..HANGUL SYLLABLE HIH
`
	got, err := parse(strings.NewReader(input), func(value string) bool { return value == "W" || value == "F" })
	if err != nil {
		t.Fatal(err)
	}
	want := []runeRange{{0x1100, 0x115F}, {0x3000, 0x3003}, {0xAC00, 0xD7A3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %X want %X", got, want)
	}
	if _, err := parse(strings.NewReader("12G4 ; W\n"), func(string) bool { return true }); err == nil {
		t.Fatal("a malformed code point was accepted")
	}
}

func TestInputsMustMatchThePinnedChecksums(t *testing.T) {
	dir := t.TempDir()
	for _, file := range pinned {
		if err := os.WriteFile(filepath.Join(dir, file.name), []byte("0000 ; W\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := run([]string{"-ucd", dir, "-check"}); err == nil || !strings.Contains(err.Error(), "not the pinned") {
		t.Fatalf("err=%v", err)
	}
}

// With the pinned Unicode files in OWNGIT_UCD_DIR, the checked-in table must
// be exactly what they generate. Without them the test is skipped, since the
// files are not part of the repository.
func TestCheckedInTableMatchesThePinnedData(t *testing.T) {
	dir := os.Getenv("OWNGIT_UCD_DIR")
	if dir == "" {
		t.Skip("set OWNGIT_UCD_DIR to a folder with the pinned Unicode files")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir("../..")
	if err := run([]string{"-ucd", absolute, "-check"}); err != nil {
		t.Fatal(err)
	}
}
