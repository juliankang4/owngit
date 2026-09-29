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
