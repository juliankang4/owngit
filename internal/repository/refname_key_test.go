package repository

import "testing"

// RefNameKey makes equal every pair of ref names that a supported file
// system opens as one file, and keeps apart names that differ in more.
func TestRefNameKeyMatchesNamesAFileSystemTreatsAsOne(t *testing.T) {
	same := [][2]string{
		{"refs/heads/main", "refs/heads/MAIN"},
		{"refs/heads/main", "refs/heads/Main"},
		{"refs/heads/café", "refs/heads/cafe\u0301"},                   // composed and decomposed é (APFS, HFS+)
		{"refs/heads/CAFÉ", "refs/heads/cafe\u0301"},                   // and letter case
		{"refs/heads/기본", "refs/heads/\u1100\u1175\u1107\u1169\u11ab"}, // Hangul syllables and conjoining jamo
		{"refs/heads/master", "refs/heads/ma\u017fter"},                // long s (APFS, NTFS)
		{"refs/heads/strasse", "refs/heads/straße"},                    // sharp s (APFS full folding)
		{"refs/heads/STRASSE", "refs/heads/stra\u1e9ee"},               // capital sharp s
		{"refs/heads/list", "refs/heads/l\u0131st"},                    // dotless i (NTFS upper-case table)
		{"refs/heads/kind", "refs/heads/\u212aind"},                    // Kelvin sign (APFS)
		{"refs/heads/ωσ", "refs/heads/ως"},                             // final sigma
		{"refs/heads/main", "refs/heads/ma\u200din"},                   // format character (HFS+ ignores it)
		{"refs/tags/v1", "refs/tags/V1"},
		{"refs/heads/Dir/x", "refs/heads/dir/x"},
	}
	for _, pair := range same {
		if RefNameKey(pair[0]) != RefNameKey(pair[1]) {
			t.Errorf("%q and %q have different keys %q and %q", pair[0], pair[1], RefNameKey(pair[0]), RefNameKey(pair[1]))
		}
	}
	different := [][2]string{
		{"refs/heads/main", "refs/heads/mains"},
		{"refs/heads/cafe", "refs/heads/café"},
		{"refs/heads/main", "refs/tags/main"},
		{"refs/heads/기본", "refs/heads/기분"},
		{"refs/heads/a", "refs/heads/\uff41"}, // full-width letters are other names everywhere
	}
	for _, pair := range different {
		if RefNameKey(pair[0]) == RefNameKey(pair[1]) {
			t.Errorf("%q and %q share the key %q", pair[0], pair[1], RefNameKey(pair[0]))
		}
	}
}

// RefNameConflicts names each written ref that shares its key with a
// different name that exists or is written too, and nothing else.
func TestRefNameConflicts(t *testing.T) {
	got := RefNameConflicts(
		[]string{"refs/heads/main", "refs/heads/café", "refs/heads/topic"},
		[]string{"refs/heads/main", "refs/heads/cafe\u0301", "refs/heads/topic", "refs/heads/new", "refs/heads/NEW", "refs/heads/other"},
	)
	want := map[string]bool{"refs/heads/cafe\u0301": true, "refs/heads/new": true, "refs/heads/NEW": true}
	if len(got) != len(want) {
		t.Fatalf("conflicts %v, want %v", got, want)
	}
	for name := range want {
		if !got[name] {
			t.Fatalf("conflicts %v, want %v", got, want)
		}
	}
}

// A name also conflicts when one of its folders matches a folder of
// another ref, or the ref itself, apart from spelling.
func TestRefNameConflictsCompareFolders(t *testing.T) {
	for _, test := range []struct {
		existing, write string
		conflict        bool
	}{
		{"refs/heads/Release/x", "refs/heads/release/main", true},
		{"refs/heads/release/main", "refs/heads/Release/x", true},
		{"refs/heads/release", "refs/heads/Release/x", true},
		{"refs/heads/Release/x", "refs/heads/release", true},
		{"refs/heads/cafe\u0301/x", "refs/heads/café/y", true},
		{"refs/heads/a/B/c", "refs/heads/a/b/d", true},
		{"refs/heads/release/x", "refs/heads/release/main", false},
		{"refs/heads/release/x", "refs/heads/releases/main", false},
		{"refs/heads/Release/x", "refs/tags/release/main", false},
		{"refs/heads/topic", "refs/heads/topic/x", false}, // Git itself refuses this one
	} {
		got := RefNameConflicts([]string{test.existing}, []string{test.write})[test.write]
		if got != test.conflict {
			t.Errorf("%q beside %q: conflict=%v, want %v", test.write, test.existing, got, test.conflict)
		}
	}
}

// HFS+ compares names with its own case table, fixed when HFS+ was
// defined, rather than current Unicode (Apple Technical Note TN1150, "Case-
// Insensitive String Comparison Algorithm":
// https://developer.apple.com/library/archive/technotes/tn/tn1150.html).
// Where that comparison differs from current Unicode folding, the classes
// are listed here as facts: HFS+ treats Georgian Asomtavruli U+10A0 to
// U+10C5 as the Mkhedruli letters U+10D0 to U+10F5, and it ignores the
// format characters U+200C to U+200F, U+202A to U+202E, U+206A to U+206F
// and U+FEFF. Each listed class shares one key, every ignored character
// has an empty key, and without the Georgian step exactly the 38 Georgian
// pairs fail.
func TestRefNameKeyMatchesHFSPlusComparison(t *testing.T) {
	georgian := 0
	for offset := rune(0); offset <= 0x25; offset++ {
		asomtavruli, mkhedruli, nuskhuri := 0x10A0+offset, 0x10D0+offset, 0x2D00+offset
		want := RefNameKey(string(mkhedruli))
		for _, member := range []rune{asomtavruli, nuskhuri} {
			if got := RefNameKey(string(member)); got != want {
				t.Errorf("HFS+ compares U+%04X as U+%04X but their keys are %q and %q", member, mkhedruli, got, want)
			}
		}
		georgian++
	}
	if georgian != 38 {
		t.Fatalf("checked %d Georgian pairs, want 38", georgian)
	}
	var ignored []rune
	for _, span := range [][2]rune{{0x200C, 0x200F}, {0x202A, 0x202E}, {0x206A, 0x206F}, {0xFEFF, 0xFEFF}} {
		for character := span[0]; character <= span[1]; character++ {
			ignored = append(ignored, character)
		}
	}
	for _, character := range ignored {
		if key := RefNameKey("ma" + string(character) + "in"); key != RefNameKey("main") {
			t.Errorf("HFS+ ignores U+%04X but %q has the key %q", character, "ma"+string(character)+"in", key)
		}
	}
	if len(ignored) != 16 {
		t.Fatalf("checked %d ignored characters, want 16", len(ignored))
	}
}
