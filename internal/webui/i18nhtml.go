package webui

import (
	"fmt"
	"html/template"
	"strings"
	"time"
)

// Bilingual rendering.
//
// Form pages must be able to change language without losing what someone has
// already typed, which rules out a reload. Rather than shipping a translation
// table and date logic to the browser, every translatable string is rendered
// here in both languages and carried on the element:
//
//	<span data-en="Branch" data-ko="브랜치">Branch</span>
//	<input data-en-placeholder="Search" data-ko-placeholder="검색" placeholder="Search">
//
// Switching then copies one attribute into place. No text is generated in
// JavaScript, so the two languages cannot drift from this catalog, and input
// values, focus, scroll position, and wizard step are never touched.
//
// Untranslated content such as a repository name, a path, or an object id is
// rendered normally and never carries these attributes.

// bi renders a translatable string as an inline span carrying both languages.
func bi(lang Lang, code MessageCode) template.HTML {
	return biText(lang, Text(LangEN, code), Text(LangKO, code))
}

// biText renders an already-resolved pair, for strings built from data such as
// a formatted date or a counted noun.
func biText(lang Lang, en, ko string) template.HTML {
	chosen := en
	if lang == LangKO {
		chosen = ko
	}
	if en == ko {
		return template.HTML(template.HTMLEscapeString(chosen))
	}
	var b strings.Builder
	b.WriteString(`<span data-en="`)
	b.WriteString(template.HTMLEscapeString(en))
	b.WriteString(`" data-ko="`)
	b.WriteString(template.HTMLEscapeString(ko))
	b.WriteString(`">`)
	b.WriteString(template.HTMLEscapeString(chosen))
	b.WriteString(`</span>`)
	return template.HTML(b.String())
}

// biAttr renders an attribute plus its two language values, for attributes
// that hold text such as placeholder, title, and aria-label.
func biAttr(lang Lang, name string, code MessageCode) template.HTMLAttr {
	return biAttrText(lang, name, Text(LangEN, code), Text(LangKO, code))
}

// biAttrText is biAttr for an already-resolved pair.
func biAttrText(lang Lang, name, en, ko string) template.HTMLAttr {
	chosen := en
	if lang == LangKO {
		chosen = ko
	}
	var b strings.Builder
	b.WriteString(template.HTMLEscapeString(name))
	b.WriteString(`="`)
	b.WriteString(template.HTMLEscapeString(chosen))
	b.WriteString(`"`)
	if en != ko {
		b.WriteString(` data-en-`)
		b.WriteString(template.HTMLEscapeString(name))
		b.WriteString(`="`)
		b.WriteString(template.HTMLEscapeString(en))
		b.WriteString(`" data-ko-`)
		b.WriteString(template.HTMLEscapeString(name))
		b.WriteString(`="`)
		b.WriteString(template.HTMLEscapeString(ko))
		b.WriteString(`"`)
	}
	return template.HTMLAttr(b.String())
}

// biRelative renders a list timestamp in both languages.
func biRelative(lang Lang, now, t time.Time) template.HTML {
	if t.IsZero() {
		return ""
	}
	return biText(lang, formatRelative(LangEN, now, t), formatRelative(LangKO, now, t))
}

// biDateTime renders an absolute timestamp in both languages.
func biDateTime(lang Lang, t time.Time) template.HTML {
	if t.IsZero() {
		return ""
	}
	return biText(lang, formatDateTime(LangEN, t), formatDateTime(LangKO, t))
}

// biDayHeading renders an activity day heading in both languages.
func biDayHeading(lang Lang, now, t time.Time) template.HTML {
	if t.IsZero() {
		return ""
	}
	return biText(lang, formatDayHeading(LangEN, now, t), formatDayHeading(LangKO, now, t))
}

// biCount renders a counted noun such as "7 repositories" in both languages.
func biCount(lang Lang, kind string, n int) template.HTML {
	return biText(lang, formatCount(LangEN, kind, n), formatCount(LangKO, kind, n))
}

// biRelease renders the new-release sentence in both languages. The versions
// are strict X.Y.Z strings chosen by the backend and are escaped like any
// other text.
func biRelease(lang Lang, n ReleaseNotice) template.HTML {
	return biText(lang,
		fmt.Sprintf(Text(LangEN, MsgReleaseAvailable), n.Version, n.Current),
		fmt.Sprintf(Text(LangKO, MsgReleaseAvailable), n.Version, n.Current))
}

// biNotice renders a notice sentence with its untranslated detail kept out of
// the swapped text, so switching language never rewrites a path or a Git
// message.
func biNotice(lang Lang, n Notice) template.HTML {
	return bi(lang, n.Code)
}

// biTitle builds the document title in both languages. The script copies the
// matching one into document.title when the language changes.
func titlePair(page Page) (string, string) {
	return documentTitle(page, LangEN), documentTitle(page, LangKO)
}
