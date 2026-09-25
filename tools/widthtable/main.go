// Command widthtable writes internal/firstrun/width_tables.go, the character
// width tables of the terminal setup, from two files of the Unicode Character
// Database. It never downloads anything: the maintainer fetches the pinned
// files once and passes their folder.
//
// Regenerate:
//
//	mkdir -p /tmp/ucd-16.0.0 && cd /tmp/ucd-16.0.0
//	curl -O https://www.unicode.org/Public/16.0.0/ucd/EastAsianWidth.txt
//	curl -O https://www.unicode.org/Public/16.0.0/ucd/extracted/DerivedCombiningClass.txt
//	cd -  # back to the repository root
//	go run ./tools/widthtable -ucd /tmp/ucd-16.0.0
//
// With -check it only compares the generated table with the checked-in file
// and fails when they differ. Both files are verified against the SHA-256
// values below before they are read. To move to a newer Unicode version,
// change unicodeVersion and both checksums together.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const unicodeVersion = "16.0.0"

// pinned are the accepted inputs and their SHA-256 checksums.
var pinned = []struct{ name, sha256 string }{
	{"EastAsianWidth.txt", "43adc76c0686a42cb370764eb8cfe2b2a45b10b855e5572a2db4a0eecce15d5b"},
	{"DerivedCombiningClass.txt", "52064d588c98c623b2373905e6a449eb520f900113954bcd212e94ef0810b471"},
}

const output = "internal/firstrun/width_tables.go"

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "widthtable: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("widthtable", flag.ContinueOnError)
	ucd := flags.String("ucd", "", "folder holding the pinned Unicode "+unicodeVersion+" files")
	check := flags.Bool("check", false, "compare with the checked-in file instead of writing it")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *ucd == "" {
		return errors.New("-ucd is required")
	}
	var inputs [2][]byte
	for i, file := range pinned {
		content, err := os.ReadFile(filepath.Join(*ucd, file.name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != file.sha256 {
			return fmt.Errorf("%s is not the pinned Unicode %s file (SHA-256 %x)", file.name, unicodeVersion, sum)
		}
		inputs[i] = content
	}
	wide, err := parse(bytes.NewReader(inputs[0]), func(value string) bool { return value == "W" || value == "F" })
	if err != nil {
		return fmt.Errorf("EastAsianWidth.txt: %w", err)
	}
	combining, err := parse(bytes.NewReader(inputs[1]), func(value string) bool { return value != "0" })
	if err != nil {
		return fmt.Errorf("DerivedCombiningClass.txt: %w", err)
	}
	generated := render(wide, combining)
	if *check {
		current, err := os.ReadFile(output)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, generated) {
			return fmt.Errorf("%s differs from the table generated from Unicode %s", output, unicodeVersion)
		}
		return nil
	}
	return os.WriteFile(output, generated, 0o644)
}

type runeRange struct{ lo, hi rune }

// parse reads a UCD property file ("0000..001F ; N # comment") and returns
// the merged ranges whose value matches. Code points the file does not list
// take its @missing default, which never matches here (N and 0).
func parse(reader io.Reader, match func(string) bool) ([]runeRange, error) {
	var ranges []runeRange
	scanner := bufio.NewScanner(reader)
	for line := 1; scanner.Scan(); line++ {
		text, _, _ := strings.Cut(scanner.Text(), "#")
		if strings.TrimSpace(text) == "" {
			continue
		}
		points, value, ok := strings.Cut(text, ";")
		if !ok {
			return nil, fmt.Errorf("line %d: no value", line)
		}
		if !match(strings.TrimSpace(value)) {
			continue
		}
		low, high, isRange := strings.Cut(strings.TrimSpace(points), "..")
		if !isRange {
			high = low
		}
		lo, err := strconv.ParseUint(low, 16, 32)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		hi, err := strconv.ParseUint(high, 16, 32)
		if err != nil || hi < lo || hi > 0x10FFFF {
			return nil, fmt.Errorf("line %d: bad range %q", line, points)
		}
		ranges = append(ranges, runeRange{rune(lo), rune(hi)})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return merge(ranges), nil
}

// merge sorts ranges and joins those that overlap or touch.
func merge(ranges []runeRange) []runeRange {
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].lo < ranges[j].lo })
	var merged []runeRange
	for _, next := range ranges {
		if n := len(merged); n > 0 && next.lo <= merged[n-1].hi+1 {
			merged[n-1].hi = max(merged[n-1].hi, next.hi)
			continue
		}
		merged = append(merged, next)
	}
	return merged
}

func table(out *strings.Builder, name string, ranges []runeRange) {
	fmt.Fprintf(out, "var %s = []runeRange{\n", name)
	for i := 0; i < len(ranges); i += 4 {
		var row []string
		for _, r := range ranges[i:min(i+4, len(ranges))] {
			row = append(row, fmt.Sprintf("{0x%X, 0x%X}", r.lo, r.hi))
		}
		fmt.Fprintf(out, "\t%s,\n", strings.Join(row, ", "))
	}
	out.WriteString("}\n")
}

func render(wide, combining []runeRange) []byte {
	var out strings.Builder
	fmt.Fprintf(&out, "// Code generated by go run ./tools/widthtable from Unicode %s data; DO NOT EDIT.\n\n", unicodeVersion)
	out.WriteString("package firstrun\n\n")
	out.WriteString("// wideRanges are the East Asian Wide and Fullwidth characters, which take\n// two terminal columns.\n")
	table(&out, "wideRanges", wide)
	out.WriteString("\n// combiningRanges are the characters with a nonzero canonical combining\n// class, which take no column of their own.\n")
	table(&out, "combiningRanges", combining)
	return []byte(out.String())
}
