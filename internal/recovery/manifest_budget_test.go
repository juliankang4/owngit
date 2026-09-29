package recovery

import (
	"bytes"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"
)

var randomTextPieces = []string{"a", "Z", "0", " ", "\t", "\n", "\"", "\\", "<", "&", "\x01", "\u2028", "한", "😀", "{", "}", "[", "]", ",", ":"}

// fillRandomly fills v with random values of its own shape.
func fillRandomly(r *rand.Rand, v reflect.Value, depth int) {
	switch v.Kind() {
	case reflect.String:
		var b strings.Builder
		for range r.IntN(12) {
			b.WriteString(randomTextPieces[r.IntN(len(randomTextPieces))])
		}
		v.SetString(b.String())
	case reflect.Bool:
		v.SetBool(r.IntN(2) == 0)
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(r.Int64N(1<<40) - 1<<39)
	case reflect.Uint, reflect.Uint64, reflect.Uint32:
		v.SetUint(r.Uint64N(1 << 40))
	case reflect.Pointer:
		if r.IntN(3) == 0 || depth > 6 {
			return
		}
		v.Set(reflect.New(v.Type().Elem()))
		fillRandomly(r, v.Elem(), depth+1)
	case reflect.Slice:
		switch r.IntN(4) {
		case 0:
			return
		case 1:
			v.Set(reflect.MakeSlice(v.Type(), 0, 0))
			return
		}
		n := r.IntN(4) + 1
		if depth > 4 {
			n = 1
		}
		s := reflect.MakeSlice(v.Type(), n, n)
		for i := range n {
			fillRandomly(r, s.Index(i), depth+1)
		}
		v.Set(s)
	case reflect.Map:
		switch r.IntN(4) {
		case 0:
			return
		case 1:
			v.Set(reflect.MakeMap(v.Type()))
			return
		}
		m := reflect.MakeMap(v.Type())
		for range r.IntN(4) + 1 {
			k := reflect.New(v.Type().Key()).Elem()
			fillRandomly(r, k, depth+1)
			e := reflect.New(v.Type().Elem()).Elem()
			fillRandomly(r, e, depth+1)
			m.SetMapIndex(k, e)
		}
		v.Set(m)
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(time.Unix(r.Int64N(4e9), r.Int64N(1e9)).UTC()))
			return
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fillRandomly(r, v.Field(i), depth+1)
			}
		}
	}
}

// Every state that backs up also restores: for random manifests (every field
// kind; nil, empty and filled lists and maps; text with whitespace, escapes
// and multibyte runes) the cost backup counts is exactly what restore
// charges, so a manifest decodes at a limit equal to its cost and not one
// below, and the decoded manifest writes the same bytes.
func TestEveryBackupFitsTheBudgetRestoreCharges(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for trial := range 1000 {
		var m Manifest
		fillRandomly(r, reflect.ValueOf(&m).Elem(), 0)
		m.Format, m.Version = backupFormat, backupVersion
		var counter manifestCounter
		if err := writeManifest(&counter, m); err != nil {
			t.Fatal(err)
		}
		cost := counter.charged + recordsCost(reflect.ValueOf(m))
		var written bytes.Buffer
		if err := writeManifest(&written, m); err != nil {
			t.Fatal(err)
		}
		size := int64(written.Len())
		decoded, err := decodeManifest(bytes.NewReader(written.Bytes()), size, cost)
		if err != nil {
			t.Fatalf("trial %d: decode at limit=cost %d: %v", trial, cost, err)
		}
		if _, err := decodeManifest(bytes.NewReader(written.Bytes()), size, cost-1); err == nil && size <= cost-1 {
			t.Fatalf("trial %d: decode at cost-1 succeeded", trial)
		}
		var again bytes.Buffer
		if err := writeManifest(&again, decoded); err != nil {
			t.Fatal(err)
		}
		if again.String() != written.String() {
			t.Fatalf("trial %d: round trip differs\n%s\n%s", trial, written.String(), again.String())
		}
	}
}
