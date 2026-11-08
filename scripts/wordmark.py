"""Writes docs/wordmark.svg and docs/wordmark-on-dark.svg: "kubeglance" in
Inter Display SemiBold converted to outlines (the files need no font), a
sparkline (the last 30 minutes) over "g" and "l", and the logo's kingfisher
(its own paths, unchanged, placed with a transform; a test checks this)
standing on the line's latest point and looking back along it.

docs/logo.svg is the master drawing of the bird; run this again whenever it
changes:

    pip install fonttools==4.60.1 uharfbuzz==0.51.1 shapely==2.0.6
    python3 scripts/wordmark.py /path/to/InterDisplay-SemiBold.otf

Inter 4.0 (SIL Open Font License 1.1; Debian/Ubuntu: fonts-inter,
otherwise https://github.com/rsms/inter/releases). Units: the ascender of
'k' is 100, the baseline is y = 0, y grows down."""
import re, sys
import uharfbuzz as hb
from fontTools.ttLib import TTFont
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.pens.boundsPen import BoundsPen
from shapely.geometry import LineString

TEXT, SPLIT = 'kubeglance', 4          # "kube" | "glance"
FONT = sys.argv[1]
NAME = {'light': '#16181d', 'dark': '#eceef1'}     # --fg in style.css
ACCENT = {'light': '#1597cc', 'dark': '#2db6e0'}   # "glance" and its line
# Positions relative to the left edge of the 'l' (x) and the baseline (y).
BIRD = (-36.6, -177.5, 73.3)  # the kingfisher's left, top and height
LINE_W = 7.3                  # the sparkline's weight
# its centre line, left to right: flat, a spike, a dip, then the flat top the
# kingfisher stands on, which stops just past the l's left edge
SPARK = [(-82.3, -91.0), (-61.6, -91.0), (-42.2, -116.0), (-23.8, -86.9), (-9.2, -114.1), (7.5, -114.1)]

icon = open('docs/logo.svg').read()
paths = re.findall(r'<path fill="(#[0-9a-f]{6})" d="([^"]+)"/>', icon)
nums = [float(v) for _, d in paths for v in re.findall(r'-?\d+\.?\d*', d)]
my0, my1 = min(nums[1::2]), max(nums[1::2])
mx0 = min(nums[0::2])
font = TTFont(FONT)
gs = font.getGlyphSet()
hbf = hb.Font(hb.Face(hb.Blob.from_file_path(FONT)))
buf = hb.Buffer(); buf.add_str(TEXT); buf.guess_segment_properties()
hb.shape(hbf, buf, {'kern': True, 'liga': True})
names = [hbf.glyph_to_string(i.codepoint) for i in buf.glyph_infos]
bp = BoundsPen(gs); gs['k'].draw(bp)
s = 100 / bp.bounds[3]

ntos = lambda v: f'{v:.2f}'.rstrip('0').rstrip('.')
pens = [SVGPathPen(gs, ntos=ntos), SVGPathPen(gs, ntos=ntos)]
tb = BoundsPen(gs)
x = 0
for i, (name, pos) in enumerate(zip(names, buf.glyph_positions)):
    t = (s, 0, 0, -s, (x + pos.x_offset) * s, -pos.y_offset * s)
    gs[name].draw(TransformPen(pens[i >= SPLIT], t))
    gs[name].draw(TransformPen(tb, t))
    if name == 'l':
        lb = BoundsPen(gs); gs[name].draw(TransformPen(lb, t))
    x += pos.x_advance
tx0, ty0, tx1, ty1 = tb.bounds
lx0 = lb.bounds[0]

# the sparkline: round ends and corners, on its own above the letters
line = LineString([(lx0 + dx, y) for dx, y in SPARK]).buffer(
    LINE_W / 2, cap_style='round', join_style='round', quad_segs=12)
spark = 'M' + 'L'.join(f'{ntos(a)} {ntos(b)}' for a, b in list(line.exterior.coords)[:-1]) + 'Z'

# the bird: its own paths, scaled and placed; drawn last, its feet on the line
k = BIRD[2] / (my1 - my0)
ox, oy = lx0 + BIRD[0] - mx0 * k, BIRD[1] - my0 * k
x0, y0 = min(tx0, line.bounds[0]), min(BIRD[1], line.bounds[1])
x1, y1 = tx1, max(ty1, 0)
marks = '\n'.join(f'    <path fill="{c}" d="{d}"/>' for c, d in paths)
bird = f'  <g transform="translate({ox:.4f} {oy:.4f}) scale({k:.6f})">\n{marks}\n  </g>'
kube, glance = pens[0].getCommands(), pens[1].getCommands() + spark


def svg(vb, name, accent):
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="{vb}">
  <path fill="{name}" d="{kube}"/>
  <path fill="{accent}" d="{glance}"/>
{bird}
</svg>
'''


vb = f'{x0:.2f} {y0:.2f} {x1 - x0:.2f} {y1 - y0:.2f}'
open('docs/wordmark.svg', 'w').write(svg(vb, NAME['light'], ACCENT['light']))
open('docs/wordmark-on-dark.svg', 'w').write(svg(vb, NAME['dark'], ACCENT['dark']))

