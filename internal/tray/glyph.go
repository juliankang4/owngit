package tray

import "math"

// The icon is drawn from a few strokes, so it stays sharp at every size and
// scale: the OwnGit mark (two branches joining), in one color without a
// background, with a small mark in the corner for every condition but
// Running. Stopped adds a crossed-out circle, Attention a filled dot and
// Unavailable an empty ring, so the condition never depends on color.

// point is a position in a 40 by 40 box.
type point struct{ x, y float64 }

// stroke is a round-capped line of width w through points.
type stroke struct {
	points []point
	w      float64
}

// ring is a circle outline of width w.
type ring struct {
	c    point
	r, w float64
}

// logo is the OwnGit mark.
var logo = func() []stroke {
	strokes := []stroke{{points: []point{{13, 13}, {13, 27}}, w: 3.6}}
	// The joining branch: up from 27,13 to 27,16, then a cubic curve to
	// 13,27 through 27,23 and 13,19.
	curve := []point{{27, 13}, {27, 16}}
	for step := 1; step <= 16; step++ {
		t := float64(step) / 16
		u := 1 - t
		curve = append(curve, point{
			u*u*u*27 + 3*u*u*t*27 + 3*u*t*t*13 + t*t*t*13,
			u*u*u*16 + 3*u*u*t*23 + 3*u*t*t*19 + t*t*t*27,
		})
	}
	return append(strokes, stroke{points: curve, w: 3.6})
}()

var logoRings = []ring{{point{13, 11}, 3, 3.6}, {point{27, 11}, 3, 3.6}, {point{13, 29}, 3, 3.6}}

// markCenter and markRadius place the condition mark; markGap clears the
// logo around it.
var (
	markCenter = point{32, 32}
	markRadius = 7.5
	markGap    = 9.5
)

// Glyph returns the icon for condition at size by size pixels, as the
// coverage of each pixel from 0 to 255, row by row.
func Glyph(condition Condition, size int) []uint8 {
	dimmed := condition == Stopped || condition == Unavailable
	return raster(size, 40, func(p point) float64 {
		if condition != Running && distance(p, markCenter) <= markGap {
			switch condition {
			case Attention:
				return inside(distance(p, markCenter) <= markRadius)
			case Stopped:
				return inside(onRing(p, ring{markCenter, markRadius - 1, 2}) ||
					onStroke(p, stroke{points: []point{{28.5, 35.5}, {35.5, 28.5}}, w: 2.4}))
			default:
				return inside(onRing(p, ring{markCenter, markRadius - 1, 2}))
			}
		}
		if !onLogo(p) {
			return 0
		}
		if dimmed {
			return 0.55
		}
		return 1
	})
}

// Symbol returns the small symbol that stands beside the condition's name
// in the panel, at size by size pixels, as Glyph does.
func Symbol(condition Condition, size int) []uint8 {
	strokes, rings := symbolShapes(condition)
	return raster(size, 16, func(p point) float64 {
		for _, r := range rings {
			if onRing(p, r) {
				return 1
			}
		}
		for _, s := range strokes {
			if onStroke(p, s) {
				return 1
			}
		}
		return 0
	})
}

// symbolShapes are the strokes and rings of the condition's symbol in a
// 16 by 16 box.
func symbolShapes(condition Condition) ([]stroke, []ring) {
	circle := ring{point{8, 8}, 5.7, 1.5}
	switch condition {
	case Running:
		return []stroke{{points: []point{{5.4, 8.2}, {7.3, 10.1}, {10.6, 6.2}}, w: 1.5}}, []ring{circle}
	case Attention:
		return []stroke{
			{points: []point{{8, 2.6}, {13.6, 12.6}, {2.4, 12.6}, {8, 2.6}}, w: 1.5},
			{points: []point{{8, 6.2}, {8, 8.7}}, w: 1.5},
			{points: []point{{8, 10.8}, {8, 10.81}}, w: 2},
		}, nil
	case Stopped:
		return []stroke{{points: []point{{4.3, 11.7}, {11.7, 4.3}}, w: 1.5}}, []ring{circle}
	default:
		return []stroke{{points: []point{{5.5, 8}, {10.5, 8}}, w: 1.5}}, []ring{circle}
	}
}

// Tile returns the OwnGit app tile of the panel's heading at size by size
// pixels: the logo in white on the brand blue rounded square. Each pixel is
// four bytes, blue, green, red and alpha, premultiplied, as Windows bitmaps
// hold them.
func Tile(size int) []uint8 {
	const blueR, blueG, blueB = 0x0A, 0x62, 0xC9
	square := raster(size, 40, func(p point) float64 { return inside(inRoundedSquare(p, 40, 10)) })
	white := raster(size, 40, func(p point) float64 { return inside(onLogo(p)) })
	pixels := make([]uint8, 4*size*size)
	for index := range square {
		alpha := float64(square[index]) / 255
		logo := float64(white[index]) / 255
		mix := func(background float64) uint8 {
			return uint8(math.Round((background*(1-logo) + 255*logo) * alpha))
		}
		pixels[4*index], pixels[4*index+1], pixels[4*index+2] = mix(blueB), mix(blueG), mix(blueR)
		pixels[4*index+3] = square[index]
	}
	return pixels
}

func inside(in bool) float64 {
	if in {
		return 1
	}
	return 0
}

func onLogo(p point) bool {
	for _, s := range logo {
		if onStroke(p, s) {
			return true
		}
	}
	for _, r := range logoRings {
		if onRing(p, r) {
			return true
		}
	}
	return false
}

// raster samples shade, which gives the coverage of a point in a box of
// side units, four by four times in each of size by size pixels.
func raster(size int, side float64, shade func(point) float64) []uint8 {
	const samples = 4
	scale := side / float64(size)
	coverage := make([]uint8, size*size)
	for y := range size {
		for x := range size {
			total := 0.0
			for sy := range samples {
				for sx := range samples {
					total += shade(point{
						(float64(x) + (float64(sx)+0.5)/samples) * scale,
						(float64(y) + (float64(sy)+0.5)/samples) * scale,
					})
				}
			}
			coverage[y*size+x] = uint8(math.Round(255 * total / samples / samples))
		}
	}
	return coverage
}

func distance(a, b point) float64 { return math.Hypot(a.x-b.x, a.y-b.y) }

func onRing(p point, r ring) bool {
	return math.Abs(distance(p, r.c)-r.r) <= r.w/2
}

func onStroke(p point, s stroke) bool {
	for index := 1; index < len(s.points); index++ {
		if segmentDistance(p, s.points[index-1], s.points[index]) <= s.w/2 {
			return true
		}
	}
	return false
}

func segmentDistance(p, a, b point) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	length := dx*dx + dy*dy
	if length == 0 {
		return distance(p, a)
	}
	t := math.Max(0, math.Min(1, ((p.x-a.x)*dx+(p.y-a.y)*dy)/length))
	return distance(p, point{a.x + t*dx, a.y + t*dy})
}

func inRoundedSquare(p point, side, radius float64) bool {
	cx := math.Max(radius, math.Min(side-radius, p.x))
	cy := math.Max(radius, math.Min(side-radius, p.y))
	return p.x >= 0 && p.y >= 0 && p.x <= side && p.y <= side && distance(p, point{cx, cy}) <= radius
}
