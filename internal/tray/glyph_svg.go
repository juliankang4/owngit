package tray

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The icon and the panel's symbols as SVG, for desktops that draw icons
// from files: the same strokes as Glyph and Symbol, turned into filled
// outlines. Desktop bars recolor a "symbolic" icon by replacing the fill of
// its shapes with their own text color, so the icon is drawn from fills
// only: every stroke is its segments and round ends, every ring two circles
// of opposite direction, all in one path with the nonzero rule.

// symbolicFill is the usual color of symbolic icons; desktops replace it.
const symbolicFill = "#bebebe"

// GlyphSVG is the icon for condition as a symbolic SVG, as Glyph draws it.
func GlyphSVG(condition Condition) string {
	var body strings.Builder
	logoPath := outline(logo, logoRings)
	opacity := ""
	if condition == Stopped || condition == Unavailable {
		opacity = ` opacity="0.55"`
	}
	if condition == Running {
		fmt.Fprintf(&body, `<path fill="%s"%s d="%s"/>`, symbolicFill, opacity, logoPath)
	} else {
		// The logo leaves a gap around the mark in the corner.
		fmt.Fprintf(&body, `<clipPath id="gap"><path clip-rule="evenodd" d="M0 0H40V40H0Z%s"/></clipPath>`, circle(markCenter, markGap, true))
		fmt.Fprintf(&body, `<path clip-path="url(#gap)" fill="%s"%s d="%s"/>`, symbolicFill, opacity, logoPath)
		var mark string
		switch condition {
		case Attention:
			mark = circle(markCenter, markRadius, true)
		case Stopped:
			mark = outline([]stroke{{points: []point{{28.5, 35.5}, {35.5, 28.5}}, w: 2.4}}, []ring{{markCenter, markRadius - 1, 2}})
		default:
			mark = outline(nil, []ring{{markCenter, markRadius - 1, 2}})
		}
		fmt.Fprintf(&body, `<path fill="%s" d="%s"/>`, symbolicFill, mark)
	}
	return svgDocument(40, body.String())
}

// SymbolSVG is the small symbol beside the condition's name, as Symbol
// draws it, as a symbolic SVG.
func SymbolSVG(condition Condition) string {
	strokes, rings := symbolShapes(condition)
	return svgDocument(16, fmt.Sprintf(`<path fill="%s" d="%s"/>`, symbolicFill, outline(strokes, rings)))
}

// TileSVG is the OwnGit app tile of the panel's heading: the logo in white
// on the brand blue rounded square, as Tile draws it.
func TileSVG() string {
	return svgDocument(40, `<rect width="40" height="40" rx="10" fill="#0a62c9"/><path fill="#ffffff" d="`+outline(logo, logoRings)+`"/>`)
}

func svgDocument(side int, body string) string {
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">%s</svg>`+"\n", side, side, side, side, body)
}

// outline is the path of strokes and rings as filled shapes, every part
// drawn clockwise, so the nonzero rule fills their union. A ring's inner
// circle runs the other way, which leaves its hole open where nothing else
// covers it.
func outline(strokes []stroke, rings []ring) string {
	var path strings.Builder
	for _, s := range strokes {
		for index, p := range s.points {
			path.WriteString(circle(p, s.w/2, true))
			if index == 0 {
				continue
			}
			a := s.points[index-1]
			length := distance(a, p)
			if length == 0 {
				continue
			}
			nx, ny := -(p.y-a.y)/length*s.w/2, (p.x-a.x)/length*s.w/2
			corners := []point{{a.x + nx, a.y + ny}, {p.x + nx, p.y + ny}, {p.x - nx, p.y - ny}, {a.x - nx, a.y - ny}}
			if area(corners) < 0 {
				corners[0], corners[1], corners[2], corners[3] = corners[3], corners[2], corners[1], corners[0]
			}
			path.WriteString("M" + coordinates(corners[0]))
			for _, corner := range corners[1:] {
				path.WriteString("L" + coordinates(corner))
			}
			path.WriteString("Z")
		}
	}
	for _, r := range rings {
		path.WriteString(circle(r.c, r.r+r.w/2, true))
		path.WriteString(circle(r.c, r.r-r.w/2, false))
	}
	return path.String()
}

// circle is a closed circle of radius r around c, clockwise on the screen
// or the other way.
func circle(c point, r float64, clockwise bool) string {
	sweep := "0"
	if clockwise {
		sweep = "1"
	}
	radius := number(r)
	return "M" + coordinates(point{c.x - r, c.y}) +
		"A" + radius + " " + radius + " 0 1 " + sweep + " " + coordinates(point{c.x + r, c.y}) +
		"A" + radius + " " + radius + " 0 1 " + sweep + " " + coordinates(point{c.x - r, c.y}) + "Z"
}

// area is the signed area of a polygon, positive when it runs clockwise on
// the screen, where y grows downward.
func area(corners []point) float64 {
	total := 0.0
	for index, a := range corners {
		b := corners[(index+1)%len(corners)]
		total += a.x*b.y - b.x*a.y
	}
	return total / 2
}

func coordinates(p point) string { return number(p.x) + " " + number(p.y) }

func number(value float64) string {
	return strconv.FormatFloat(math.Round(value*1000)/1000, 'f', -1, 64)
}
