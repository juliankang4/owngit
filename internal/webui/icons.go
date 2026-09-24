package webui

import "html/template"

// Icons are inline SVG so they inherit text color and need no extra request.
// Every shape is authored here; none of it comes from repository content.
//
// Status shapes differ from each other, so a state never depends on color
// alone. Decorative icons carry aria-hidden and sit next to real text.
var iconPaths = map[string]string{
	"branch":   `<circle cx="4.5" cy="3.5" r="1.9"/><circle cx="4.5" cy="12.5" r="1.9"/><circle cx="11.5" cy="5.5" r="1.9"/><path d="M4.5 5.4v5.2M11.5 7.4c0 3.1-7 1.6-7 5.1" stroke-linecap="round"/>`,
	"tag":      `<path d="M8.6 2.2H13v4.4l-6 6-4.4-4.4z" stroke-linejoin="round"/><circle cx="10.6" cy="4.6" r="0.9"/>`,
	"commit":   `<circle cx="8" cy="8" r="2.6"/><path d="M8 2.2v3.2M8 10.6v3.2" stroke-linecap="round"/>`,
	"folder":   `<path d="M2.2 4.4h3.9l1.3 1.6h6.4v6.8H2.2z" stroke-linejoin="round"/>`,
	"file":     `<path d="M4.2 2.2h5l3 3v8.6h-8z" stroke-linejoin="round"/><path d="M9.1 2.3v3.1h3" stroke-linejoin="round"/>`,
	"symlink":  `<path d="M4.2 2.2h5l3 3v8.6h-8z" stroke-linejoin="round"/><path d="M5.8 10.4l4-4M7.4 6.4h2.4v2.4" stroke-linecap="round" stroke-linejoin="round"/>`,
	"module":   `<path d="M8 2.4l5 2.6v6l-5 2.6-5-2.6v-6z" stroke-linejoin="round"/><path d="M3 5l5 2.6 5-2.6M8 7.6V14" stroke-linejoin="round"/>`,
	"lock":     `<rect x="3.4" y="7.1" width="9.2" height="6.4" rx="1.5"/><path d="M5.6 7V5.2a2.4 2.4 0 014.8 0V7" stroke-linecap="round"/>`,
	"unlocked": `<rect x="3.4" y="7.1" width="9.2" height="6.4" rx="1.5"/><path d="M5.6 7V5.2a2.4 2.4 0 014.8-.5" stroke-linecap="round"/>`,
	"warning":  `<path d="M8 2.6l5.6 10H2.4z" stroke-linejoin="round"/><path d="M8 6.4v3.1" stroke-linecap="round"/><path d="M8 11.4h.01" stroke-width="2.1" stroke-linecap="round"/>`,
	"info":     `<circle cx="8" cy="8" r="5.7"/><path d="M8 7.2v3.6" stroke-linecap="round"/><path d="M8 5h.01" stroke-width="2" stroke-linecap="round"/>`,
	"check":    `<circle cx="8" cy="8" r="5.7"/><path d="M5.4 8.2l1.9 1.9 3.3-3.9" stroke-linecap="round" stroke-linejoin="round"/>`,
	"error":    `<rect x="2.4" y="2.4" width="11.2" height="11.2" rx="1.6"/><path d="M8 5.2v3.4" stroke-linecap="round"/><path d="M8 11.1h.01" stroke-width="2.1" stroke-linecap="round"/>`,
	"kept":     `<path d="M3.2 4.2h9.6v8.4H3.2z" stroke-linejoin="round"/><path d="M3.2 6.8h9.6M6.4 3v1.2M9.6 3v1.2" stroke-linecap="round"/>`,
	"plus":     `<path d="M8 3.4v9.2M3.4 8h9.2" stroke-linecap="round"/>`,
	// Navigation.
	"home":     `<path d="M2.6 7.4L8 2.8l5.4 4.6" stroke-linecap="round" stroke-linejoin="round"/><path d="M4.2 6.4v6.8h7.6V6.4" stroke-linejoin="round"/>`,
	"activity": `<path d="M1.8 8.4h2.6l1.7-4 3.2 8 1.7-4h3.2" stroke-linecap="round" stroke-linejoin="round"/>`,
	"repo":     `<path d="M3.6 2.6h8.6v10.2H4.9a1.3 1.3 0 01-1.3-1.3z" stroke-linejoin="round"/><path d="M3.6 11.5a1.3 1.3 0 011.3-1.3h7.3" stroke-linejoin="round"/>`,
	"pr":       `<circle cx="4.5" cy="3.8" r="1.9"/><circle cx="4.5" cy="12.2" r="1.9"/><circle cx="11.5" cy="12.2" r="1.9"/><path d="M4.5 5.7v4.6M11.5 10.3V6.4a2 2 0 00-2-2H8" stroke-linecap="round"/>`,
	"import":   `<path d="M8 2.4v7.2M5 6.8L8 9.8l3-3M2.8 11.2v2h10.4v-2" stroke-linecap="round" stroke-linejoin="round"/>`,
	"menu":     `<path d="M2.8 4.4h10.4M2.8 8h10.4M2.8 11.6h10.4" stroke-linecap="round"/>`,
	"chevron":  `<path d="M4.4 6.2L8 9.8l3.6-3.6" stroke-linecap="round" stroke-linejoin="round"/>`,
	"panel":    `<rect x="2.2" y="2.8" width="11.6" height="10.4" rx="1.4"/><path d="M6.2 2.8v10.4"/>`,
	"wrap":     `<path d="M2.4 4h11.2M2.4 8h9a2 2 0 010 4H8.6" stroke-linecap="round" stroke-linejoin="round"/><path d="M10 10.6L8.6 12l1.4 1.4" stroke-linecap="round" stroke-linejoin="round"/><path d="M2.4 12h3.4" stroke-linecap="round"/>`,
	"copy":     `<rect x="5.4" y="5.4" width="8" height="8" rx="1.4"/><path d="M10.6 5.2V3.8a1.2 1.2 0 00-1.2-1.2H3.8a1.2 1.2 0 00-1.2 1.2v5.6a1.2 1.2 0 001.2 1.2h1.4" stroke-linecap="round"/>`,
	"trash":    `<path d="M2.8 4.4h10.4M6.4 4.2V2.8h3.2v1.4M4.2 4.4l.7 9h6.2l.7-9" stroke-linecap="round" stroke-linejoin="round"/><path d="M6.8 7v4M9.2 7v4" stroke-linecap="round"/>`,
	// Evidence states. Each shape differs from the others, so a state is never
	// carried by colour alone: a stopped run, an unavailable one, a stale one
	// and an absent one are four distinct outlines.
	"stop":     `<rect x="2.6" y="2.6" width="10.8" height="10.8" rx="2.4"/><path d="M6.1 6.1h3.8v3.8H6.1z" stroke-linejoin="round"/>`,
	"slash":    `<circle cx="8" cy="8" r="5.7"/><path d="M4.3 11.7L11.7 4.3" stroke-linecap="round"/>`,
	"clock":    `<circle cx="8" cy="8" r="5.7"/><path d="M8 4.8V8l2.4 1.6" stroke-linecap="round" stroke-linejoin="round"/>`,
	"minus":    `<circle cx="8" cy="8" r="5.7"/><path d="M5.4 8h5.2" stroke-linecap="round"/>`,
	"merge":    `<circle cx="4.5" cy="3.8" r="1.9"/><circle cx="4.5" cy="12.2" r="1.9"/><circle cx="11.5" cy="8" r="1.9"/><path d="M4.5 5.7v4.6M6.4 3.8h1.2a2 2 0 012 2v.3" stroke-linecap="round"/>`,
	"key":      `<circle cx="5.6" cy="10.4" r="2.6"/><path d="M7.5 8.5l5-5M10.6 3.2h2.6v2.6" stroke-linecap="round" stroke-linejoin="round"/>`,
	"search":   `<circle cx="7.2" cy="7.2" r="4.3"/><path d="M10.4 10.4l3 3" stroke-linecap="round"/>`,
	"settings": `<circle cx="8" cy="8" r="2.2"/><path d="M8 1.9v1.6M8 12.5v1.6M1.9 8h1.6M12.5 8h1.6M3.7 3.7l1.1 1.1M11.2 11.2l1.1 1.1M12.3 3.7l-1.1 1.1M4.8 11.2l-1.1 1.1" stroke-linecap="round"/>`,
	"back":     `<path d="M9.6 3.6L5.2 8l4.4 4.4" stroke-linecap="round" stroke-linejoin="round"/>`,
	"up":       `<path d="M8 12.4V4.2M4.4 7.6L8 4l3.6 3.6" stroke-linecap="round" stroke-linejoin="round"/>`,
	"light":    `<circle cx="8" cy="8" r="3.1"/><path d="M8 1.4v1.7M8 12.9v1.7M1.4 8h1.7M12.9 8h1.7M3.3 3.3l1.2 1.2M11.5 11.5l1.2 1.2M12.7 3.3l-1.2 1.2M4.5 11.5l-1.2 1.2" stroke-linecap="round"/>`,
	"dark":     `<path d="M13.2 9.6A5.6 5.6 0 016.4 2.8a5.7 5.7 0 106.8 6.8z" stroke-linejoin="round"/>`,
	"system":   `<rect x="1.8" y="3" width="12.4" height="8.4" rx="1.4"/><path d="M5.6 13.8h4.8" stroke-linecap="round"/>`,
}

// icon renders one inline SVG by name. An unknown name renders nothing rather
// than a broken glyph.
func icon(name string) template.HTML {
	path, ok := iconPaths[name]
	if !ok {
		return ""
	}
	return template.HTML(`<svg class="icon" width="14" height="14" viewBox="0 0 16 16" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="1.5">` + path + `</svg>`)
}

// statusIcon maps a notice kind to its shape.
func statusIcon(kind NoticeKind) template.HTML {
	switch kind {
	case NoticeError:
		return icon("error")
	case NoticeWarning:
		return icon("warning")
	case NoticeSuccess:
		return icon("check")
	default:
		return icon("info")
	}
}

// themeIcon maps an appearance option to its shape.
func themeIcon(name string) template.HTML { return icon(name) }
