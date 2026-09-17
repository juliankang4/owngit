package webui

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Colour is never the only carrier of meaning, but where text sits on a
// surface it still has to be readable. Both palettes are checked against the
// WCAG 2 contrast formula so a later palette edit cannot quietly break a
// status colour or a small label.

var (
	blockPattern = regexp.MustCompile(`(?s)\{(.*?)\n\}`)
	tokenPattern = regexp.MustCompile(`(--[\w-]+):\s*(#[0-9a-fA-F]{6})`)
)

func palette(t *testing.T, selector string) map[string]string {
	t.Helper()
	data, err := assetFS.ReadFile("assets/owngit.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(data)
	start := strings.Index(css, selector)
	if start < 0 {
		t.Fatalf("palette %q not found", selector)
	}
	block := blockPattern.FindStringSubmatch(css[start:])
	if block == nil {
		t.Fatalf("palette %q is not a complete rule", selector)
	}
	out := map[string]string{}
	for _, m := range tokenPattern.FindAllStringSubmatch(block[1], -1) {
		out[m[1]] = m[2]
	}
	if len(out) == 0 {
		t.Fatalf("palette %q defines no colours", selector)
	}
	return out
}

func relativeLuminance(hex string) float64 {
	channel := func(offset int) float64 {
		v, err := strconv.ParseInt(hex[offset:offset+2], 16, 0)
		if err != nil {
			return 0
		}
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(1) + 0.7152*channel(3) + 0.0722*channel(5)
}

func contrast(a, b string) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}

// labOf converts a hex colour to CIELAB. Adjacent steps of the activity ramp
// differ partly in hue rather than lightness, so a luminance ratio is the
// wrong measure for them; perceptual distance is what decides whether two
// neighbouring cells can be told apart.
func labOf(hex string) [3]float64 {
	linear := func(offset int) float64 {
		v, err := strconv.ParseInt(hex[offset:offset+2], 16, 0)
		if err != nil {
			return 0
		}
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	r, g, b := linear(1), linear(3), linear(5)

	x := (r*0.4124 + g*0.3576 + b*0.1805) / 0.95047
	y := r*0.2126 + g*0.7152 + b*0.0722
	z := (r*0.0193 + g*0.1192 + b*0.9505) / 1.08883

	f := func(t float64) float64 {
		if t > 0.008856 {
			return math.Cbrt(t)
		}
		return 7.787*t + 16.0/116.0
	}
	fx, fy, fz := f(x), f(y), f(z)
	return [3]float64{116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)}
}

// deltaE is the CIE76 perceptual distance between two colours.
func deltaE(a, b string) float64 {
	la, lb := labOf(a), labOf(b)
	var sum float64
	for i := range la {
		d := la[i] - lb[i]
		sum += d * d
	}
	return math.Sqrt(sum)
}

func TestTextContrastInBothPalettes(t *testing.T) {
	// Small type is used throughout the accepted design, which makes these the
	// normal-text thresholds rather than the large-text ones.
	pairs := []struct {
		what     string
		fg, bg   string
		required float64
	}{
		{"body text on content", "--text-1", "--bg-content", 4.5},
		{"secondary text on content", "--text-2", "--bg-content", 4.5},
		{"quiet text on content", "--text-3", "--bg-content", 4.5},
		{"quiet text on a filled chip", "--text-3", "--bg-fill", 4.5},
		{"quiet text on a hovered row", "--text-3", "--bg-fill-2", 4.5},
		{"secondary text in the sidebar", "--text-2", "--bg-sidebar", 4.5},
		{"link on content", "--accent", "--bg-content", 4.5},
		{"link on a filled surface", "--accent", "--bg-fill", 4.5},
		{"link on the accent tint", "--accent", "--accent-soft", 4.5},
		{"button label on the accent", "--on-accent", "--accent", 4.5},
		{"success text", "--ok", "--bg-content", 4.5},
		{"warning text", "--warn", "--bg-content", 4.5},
		{"error text", "--bad", "--bg-content", 4.5},
		{"success text on its notice", "--ok", "--ok-soft", 4.5},
		{"warning text on its notice", "--warn", "--warn-soft", 4.5},
		{"error text on its notice", "--bad", "--bad-soft", 4.5},
		{"code on the code surface", "--text-1", "--code-bg", 4.5},
		{"line numbers in the gutter", "--text-3", "--code-gutter", 4.5},
		{"code on an added diff row", "--text-1", "--diff-add-bg", 4.5},
		{"code on a removed diff row", "--text-1", "--diff-del-bg", 4.5},
		// The busiest activity step is a graphic, not text.
		{"busiest activity step", "--hm-4", "--bg-content", 3.0},
	}

	for _, p := range []struct {
		name     string
		selector string
	}{
		{"light", ":root {"},
		{"dark", ".theme-dark {"},
	} {
		colours := palette(t, p.selector)
		for _, pair := range pairs {
			fg, okFG := colours[pair.fg]
			bg, okBG := colours[pair.bg]
			if !okFG || !okBG {
				t.Errorf("%s palette: %s uses an undefined token (%s on %s)", p.name, pair.what, pair.fg, pair.bg)
				continue
			}
			if got := contrast(fg, bg); got < pair.required {
				t.Errorf("%s palette: %s is %s, need %s",
					p.name, pair.what,
					fmt.Sprintf("%.2f:1", got), fmt.Sprintf("%.2f:1", pair.required))
			}
		}
	}
}

func TestSystemPaletteMatchesTheDarkPalette(t *testing.T) {
	// System appearance must be the same Dark palette, not a second one that
	// can drift from it.
	dark := palette(t, ".theme-dark {")
	system := palette(t, "  .theme-system {")

	for token, want := range dark {
		got, ok := system[token]
		if !ok {
			t.Errorf("System appearance is missing %s", token)
			continue
		}
		if got != want {
			t.Errorf("System appearance defines %s as %s, Dark uses %s", token, got, want)
		}
	}
}

func TestActivityRampIsDistinguishableAndNotAStatusColour(t *testing.T) {
	for _, p := range []struct{ name, selector string }{
		{"light", ":root {"},
		{"dark", ".theme-dark {"},
	} {
		colours := palette(t, p.selector)
		steps := []string{"--hm-0", "--hm-1", "--hm-2", "--hm-3", "--hm-4"}
		// A CIE76 distance above about 10 is a difference a reader notices
		// between two small adjacent swatches.
		const minStep = 10.0
		for i := 1; i < len(steps); i++ {
			prev, cur := colours[steps[i-1]], colours[steps[i]]
			if prev == "" || cur == "" {
				t.Fatalf("%s palette: the activity ramp is incomplete", p.name)
			}
			if got := deltaE(prev, cur); got < minStep {
				t.Errorf("%s palette: %s and %s are too close to tell apart (deltaE %.1f, need %.0f)",
					p.name, steps[i-1], steps[i], got, minStep)
			}
		}
		// The quietest step must also be visible against the page itself.
		if got := deltaE(colours["--hm-0"], colours["--bg-content"]); got < 3 {
			t.Errorf("%s palette: an empty activity day is invisible against the page (deltaE %.1f)", p.name, got)
		}
		// The ramp is blue so it never reads as a green "checks passed" signal.
		for _, step := range steps[1:] {
			hex := colours[step]
			r, _ := strconv.ParseInt(hex[1:3], 16, 0)
			g, _ := strconv.ParseInt(hex[3:5], 16, 0)
			b, _ := strconv.ParseInt(hex[5:7], 16, 0)
			if b <= g || b <= r {
				t.Errorf("%s palette: activity step %s (%s) is not blue and could read as a status", p.name, step, hex)
			}
		}
	}
}
