package ui

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The series colours must stay apart for colour-blind readers too: CPU and
// memory sit next to each other on every card. The thresholds and the maths
// are those of the dataviz validator the palette was chosen with: OKLab
// distance ×100 under the Machado, Oliveira & Fernandes (2009) severity 1.0
// protan / deutan simulation in linear RGB (target ≥ 8), the same distance
// unsimulated (floor ≥ 15), and WCAG contrast against the card.
const (
	cvdTarget   = 8.0
	normalFloor = 15.0
)

func TestSeriesColoursAreColourBlindSafe(t *testing.T) {
	css, err := static.ReadFile("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	dark, light := themes(t, string(css))
	for name, tok := range map[string]map[string]string{"dark": dark, "light": light} {
		cpu, mem, card := tok["--cpu"], tok["--mem"], tok["--card"]
		if n := deltaE(cpu, mem, nil); n < normalFloor {
			t.Errorf("%s: CPU %s and memory %s are %.1f apart (normal vision), want ≥ %.0f", name, cpu, mem, n, normalFloor)
		}
		for kind, m := range map[string]*[3][3]float64{"protan": &protan, "deutan": &deutan} {
			if d := deltaE(cpu, mem, m); d < cvdTarget {
				t.Errorf("%s: CPU %s and memory %s are %.1f apart for %s, want ≥ %.0f", name, cpu, mem, d, kind, cvdTarget)
			}
		}
		for _, k := range []string{"--cpu", "--mem"} {
			if c := contrast(tok[k], card); c < 3 {
				t.Errorf("%s: %s %s on the card %s is %.2f:1, want ≥ 3:1", name, k, tok[k], card, c)
			}
		}
		// text: primary AAA, secondary AA (phones are read outdoors)
		if c := contrast(tok["--fg"], card); c < 7 {
			t.Errorf("%s: text %s on %s is %.2f:1, want ≥ 7:1", name, tok["--fg"], card, c)
		}
		if c := contrast(tok["--muted"], card); c < 4.5 {
			t.Errorf("%s: secondary text %s on %s is %.2f:1, want ≥ 4.5:1", name, tok["--muted"], card, c)
		}
	}
}

func TestColourMathMatchesTheValidator(t *testing.T) {
	// values the dataviz validator reports for this pair
	if d := deltaE("#326ce5", "#009e73", &deutan); math.Abs(d-23.3) > 0.1 {
		t.Errorf("deutan ΔE = %.2f, validator says 23.3", d)
	}
	if d := deltaE("#326ce5", "#009e73", nil); math.Abs(d-25.3) > 0.1 {
		t.Errorf("normal ΔE = %.2f, validator says 25.3", d)
	}
	if d := deltaE("#4fa3ff", "#b08cff", &deutan); math.Abs(d-2.5) > 0.1 { // the old blue + purple
		t.Errorf("deutan ΔE = %.2f, validator says 2.5", d)
	}
}

// themes returns the custom properties of the dark :root and of the light
// override (which inherits what it does not set).
func themes(t *testing.T, css string) (dark, light map[string]string) {
	t.Helper()
	decl := regexp.MustCompile(`(--[a-z-]+):\s*(#[0-9a-fA-F]{6})`)
	parse := func(block string) map[string]string {
		m := map[string]string{}
		for _, d := range decl.FindAllStringSubmatch(block, -1) {
			m[d[1]] = strings.ToLower(d[2])
		}
		return m
	}
	i := strings.Index(css, ":root {")
	j := strings.Index(css, "@media (prefers-color-scheme: light)")
	if i < 0 || j < i {
		t.Fatal("no :root block followed by a light-scheme override in style.css")
	}
	dark = parse(css[i:j])
	end := strings.Index(css[j:], "\n}\n")
	light = maps.Clone(dark)
	maps.Copy(light, parse(css[j:j+end]))
	for _, k := range []string{"--cpu", "--mem", "--card", "--fg", "--muted"} {
		if dark[k] == "" {
			t.Fatalf("%s is not a hex colour in the dark theme", k)
		}
	}
	return dark, light
}

var (
	protan = [3][3]float64{{0.152286, 1.052583, -0.204868}, {0.114503, 0.786281, 0.099216}, {-0.003882, -0.048116, 1.051998}}
	deutan = [3][3]float64{{0.367322, 0.860646, -0.227968}, {0.280085, 0.672501, 0.047413}, {-0.011820, 0.042940, 0.968881}}
)

func linear(hex string) [3]float64 {
	var out [3]float64
	for i := range 3 {
		v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		if err != nil {
			panic(fmt.Sprintf("bad colour %q", hex))
		}
		c := float64(v) / 255
		if c <= 0.04045 {
			out[i] = c / 12.92
		} else {
			out[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return out
}

func oklab(c [3]float64) [3]float64 {
	r, g, b := c[0], c[1], c[2]
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return [3]float64{
		0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		0.0259040371*l + 0.7827717662*m - 0.8086757660*s,
	}
}

// deltaE is the OKLab distance ×100 of two colours, simulated through m
// (nil: normal vision).
func deltaE(a, b string, m *[3][3]float64) float64 {
	sim := func(h string) [3]float64 {
		c := linear(h)
		if m == nil {
			return oklab(c)
		}
		var o [3]float64
		for i := range 3 {
			o[i] = math.Max(0, math.Min(1, m[i][0]*c[0]+m[i][1]*c[1]+m[i][2]*c[2]))
		}
		return oklab(o)
	}
	x, y := sim(a), sim(b)
	return 100 * math.Sqrt((x[0]-y[0])*(x[0]-y[0])+(x[1]-y[1])*(x[1]-y[1])+(x[2]-y[2])*(x[2]-y[2]))
}

func contrast(a, b string) float64 {
	lum := func(h string) float64 { c := linear(h); return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2] }
	la, lb := lum(a), lum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
