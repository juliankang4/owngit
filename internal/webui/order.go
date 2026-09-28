package webui

import (
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ListOrder is the order of the repository lists: the dashboard list and the
// sidebar. It is a preference of one browser, like the language.
type ListOrder string

const (
	// OrderUpdatedNewest lists the most recently updated repository first.
	// It is the default.
	OrderUpdatedNewest ListOrder = "updated-desc"
	// OrderUpdatedOldest lists the least recently updated repository first.
	OrderUpdatedOldest ListOrder = "updated-asc"
	// OrderNameAsc lists names in the interface language's alphabetical
	// order.
	OrderNameAsc ListOrder = "name-asc"
	// OrderNameDesc is the exact reverse of OrderNameAsc.
	OrderNameDesc ListOrder = "name-desc"
)

// DefaultListOrder is used whenever no valid order was chosen.
const DefaultListOrder = OrderUpdatedNewest

// ListOrders returns the choices in the order the control offers them.
func ListOrders() []ListOrder {
	return []ListOrder{OrderUpdatedNewest, OrderUpdatedOldest, OrderNameAsc, OrderNameDesc}
}

// ParseListOrder validates a submitted or stored order.
func ParseListOrder(value string) (ListOrder, bool) {
	switch order := ListOrder(value); order {
	case OrderUpdatedNewest, OrderUpdatedOldest, OrderNameAsc, OrderNameDesc:
		return order, true
	default:
		return DefaultListOrder, false
	}
}

// Label is the order's name in the control and the sidebar.
func (o ListOrder) Label() MessageCode {
	switch o {
	case OrderUpdatedOldest:
		return MsgOrderUpdatedOldest
	case OrderNameAsc:
		return MsgOrderNameAsc
	case OrderNameDesc:
		return MsgOrderNameDesc
	default:
		return MsgOrderUpdatedNewest
	}
}

// ListKey is what ordering reads from one row.
type ListKey struct {
	Name string
	// ID breaks ties between equal names, so the order never depends on the
	// order the rows arrived in.
	ID string
	// Updated is the time the row shows as its last update. Zero means the
	// row shows none (no commits yet, or not readable now); such rows come
	// last in both time orders.
	Updated time.Time
}

// NameRank is a row's position in name order in each interface language. A
// page carries it so the script can reorder rows in place when the language
// changes, without a second implementation of the collation.
type NameRank struct {
	EN int
	KO int
}

// orderList sorts items for display and records each item's name positions.
//
// Time orders compare whole seconds, as the page does: Git records author
// times in seconds. Equal times, and the undated rows at the end, follow
// name order. Name order compares names as described at compareNames and
// then IDs; the descending order is its exact reverse.
func orderList[T any](items []T, order ListOrder, lang Lang, key func(T) ListKey, setRank func(*T, NameRank)) {
	keys := make([]ListKey, len(items))
	for index, item := range items {
		keys[index] = key(item)
	}
	positions := func(in Lang) []int {
		byName := make([]int, len(items))
		for index := range byName {
			byName[index] = index
		}
		sort.Slice(byName, func(left, right int) bool {
			return nameBefore(keys[byName[left]], keys[byName[right]], in)
		})
		rank := make([]int, len(items))
		for position, index := range byName {
			rank[index] = position
		}
		return rank
	}
	rankEN, rankKO := positions(LangEN), positions(LangKO)
	rank := rankEN
	if lang == LangKO {
		rank = rankKO
	}

	permutation := make([]int, len(items))
	for index := range permutation {
		permutation[index] = index
	}
	sort.Slice(permutation, func(left, right int) bool {
		a, b := permutation[left], permutation[right]
		switch order {
		case OrderNameAsc:
			return rank[a] < rank[b]
		case OrderNameDesc:
			return rank[a] > rank[b]
		}
		timeA, timeB := keys[a].Updated, keys[b].Updated
		if timeA.IsZero() != timeB.IsZero() {
			return !timeA.IsZero()
		}
		if secondsA, secondsB := timeA.Unix(), timeB.Unix(); !timeA.IsZero() && secondsA != secondsB {
			if order == OrderUpdatedOldest {
				return secondsA < secondsB
			}
			return secondsA > secondsB
		}
		return rank[a] < rank[b]
	})

	sorted := make([]T, len(items))
	for position, index := range permutation {
		sorted[position] = items[index]
		setRank(&sorted[position], NameRank{EN: rankEN[index], KO: rankKO[index]})
	}
	copy(items, sorted)
}

func nameBefore(a, b ListKey, lang Lang) bool {
	if c := compareNames(a.Name, b.Name, lang); c != 0 {
		return c < 0
	}
	return a.ID < b.ID
}

// compareNames orders names the way a reader of lang expects an
// alphabetical list:
//
//   - case is ignored;
//   - a run of digits compares by its value, so "project-2" comes before
//     "project-10";
//   - punctuation comes before digits, and digits before letters;
//   - Korean readers see Hangul (in 가나다 order, which is the order of the
//     Unicode syllable block) before Latin letters; English readers see Latin
//     letters first.
//
// It is a small, fixed subset of the Unicode collation algorithm that covers
// repository names; it has no tables for accents or other scripts, which
// keep their code point order after the scripts above.
func compareNames(a, b string, lang Lang) int {
	for a != "" && b != "" {
		if digitA, digitB := isASCIIDigit(a[0]), isASCIIDigit(b[0]); digitA && digitB {
			var runA, runB string
			runA, a = digitRun(a)
			runB, b = digitRun(b)
			if c := compareDigits(runA, runB); c != 0 {
				return c
			}
			continue
		}
		runeA, sizeA := utf8.DecodeRuneInString(a)
		runeB, sizeB := utf8.DecodeRuneInString(b)
		a, b = a[sizeA:], b[sizeB:]
		groupA, groupB := nameGroup(runeA, lang), nameGroup(runeB, lang)
		if groupA != groupB {
			return groupA - groupB
		}
		foldA, foldB := unicode.ToLower(runeA), unicode.ToLower(runeB)
		if foldA != foldB {
			if foldA < foldB {
				return -1
			}
			return 1
		}
	}
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	default:
		return 1
	}
}

// Groups in name order. Letter groups are ranked per language below.
const (
	groupPunctuation = iota
	groupDigit
	groupLetters
)

func nameGroup(r rune, lang Lang) int {
	switch {
	case r >= '0' && r <= '9':
		return groupDigit
	case !unicode.IsLetter(r):
		return groupPunctuation
	}
	hangul := unicode.Is(unicode.Hangul, r)
	latin := unicode.Is(unicode.Latin, r)
	switch {
	case lang == LangKO && hangul, lang != LangKO && latin:
		return groupLetters
	case lang == LangKO && latin, lang != LangKO && !hangul:
		return groupLetters + 1
	default:
		// Hangul for English readers, or another script for Korean readers.
		return groupLetters + 2
	}
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

func digitRun(s string) (run, rest string) {
	end := 0
	for end < len(s) && isASCIIDigit(s[end]) {
		end++
	}
	return s[:end], s[end:]
}

// compareDigits compares two digit runs by value. Runs of equal value, such
// as "7" and "007", compare equal.
func compareDigits(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

// OrderRepositories puts the dashboard list in order.
func OrderRepositories(items []RepositorySummary, order ListOrder, lang Lang) {
	orderList(items, order, lang,
		func(s RepositorySummary) ListKey { return ListKey{Name: s.Name, ID: s.ID, Updated: s.Updated()} },
		func(s *RepositorySummary, rank NameRank) { s.Rank = rank })
}

// OrderNav puts the sidebar list in order.
func OrderNav(items []NavRepository, order ListOrder, lang Lang) {
	orderList(items, order, lang,
		func(r NavRepository) ListKey { return ListKey{Name: r.Name, ID: r.ID, Updated: r.LastActivity} },
		func(r *NavRepository, rank NameRank) { r.Rank = rank })
}
