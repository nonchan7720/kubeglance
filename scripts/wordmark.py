"""Writes docs/wordmark.svg and docs/wordmark-on-dark.svg: the logo's own
paths (unchanged, placed with a transform; a test checks this) and
"kubeglance" in Inter Display SemiBold converted to outlines, so the files
need no font.

Run it again whenever docs/logo.svg changes:

    pip install fonttools==4.60.1 uharfbuzz==0.51.1
    python3 scripts/wordmark.py /path/to/InterDisplay-SemiBold.otf

Inter 4.0 (SIL Open Font License 1.1; Debian/Ubuntu: fonts-inter,
otherwise https://github.com/rsms/inter/releases). The mark is 1.32 times
the height of the 'k', stands on the baseline, and sits 0.30 of that height
before the name."""
import re, sys
import uharfbuzz as hb
from fontTools.ttLib import TTFont
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.pens.boundsPen import BoundsPen

TEXT = 'kubeglance'
FONT = sys.argv[1]
icon_path, out_light, out_dark = 'docs/logo.svg', 'docs/wordmark.svg', 'docs/wordmark-on-dark.svg'
MARK_H, GAP = 1.32, 0.30      # of the ascender of 'k'

icon = open(icon_path).read()
paths = re.findall(r'<path fill="(#[0-9a-f]{6})" d="([^"]+)"/>', icon)
nums = [float(v) for _, d in paths for v in re.findall(r'-?\d+\.?\d*', d)]
xs, ys = nums[0::2], nums[1::2]
mx0, my0, mx1, my1 = min(xs), min(ys), max(xs), max(ys)

font = TTFont(FONT)
gs = font.getGlyphSet()
blob = hb.Blob.from_file_path(FONT)
hbf = hb.Font(hb.Face(blob))
buf = hb.Buffer(); buf.add_str(TEXT); buf.guess_segment_properties()
hb.shape(hbf, buf, {'kern': True, 'liga': True})
names = [hbf.glyph_to_string(i.codepoint) for i in buf.glyph_infos]

bp = BoundsPen(gs); gs['k'].draw(bp)
asc = bp.bounds[3]                  # top of 'k' above the baseline, font units
s = 100 / asc                       # 1 unit of output = asc / 100

# text: baseline at y = 0, flipped (SVG y grows down)
pen = SVGPathPen(gs, ntos=lambda v: f'{v:.2f}'.rstrip('0').rstrip('.'))
x = 0
tb = BoundsPen(gs)
for name, pos in zip(names, buf.glyph_positions):
    t = (s, 0, 0, -s, (x + pos.x_offset) * s, -pos.y_offset * s)
    gs[name].draw(TransformPen(pen, t))
    gs[name].draw(TransformPen(tb, t))
    x += pos.x_advance
tx0, ty0, tx1, ty1 = tb.bounds

# mark: bottom on the baseline, height MARK_H × asc, GAP × asc before the text
k = MARK_H * 100 / (my1 - my0)
ox = tx0 - GAP * 100 - mx1 * k
oy = 0 - my1 * k
x0, y0 = ox + mx0 * k, oy + my0 * k
x1, y1 = tx1, max(ty1, 0)
vb = f'{x0:.2f} {y0:.2f} {x1 - x0:.2f} {y1 - y0:.2f}'

def svg(fg):
    marks = '\n'.join(f'    <path fill="{c}" d="{d}"/>' for c, d in paths)
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="{vb}">
  <g transform="translate({ox:.4f} {oy:.4f}) scale({k:.6f})">
{marks}
  </g>
  <path fill="{fg}" d="{pen.getCommands()}"/>
</svg>
'''
open(out_light, 'w').write(svg('#16181d'))
open(out_dark, 'w').write(svg('#eceef1'))
