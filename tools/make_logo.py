"""Render the SDRg35xx app logo (v3).

An RG35XX Plus handheld (vertical Game Boy-style body: screen on top,
D-pad left, ABXY right) whose screen shows the app's own spectrum +
waterfall, with a telescopic antenna on the top-right corner radiating
radio waves. Drawn at 4x and downsampled for smooth edges.

    python tools/make_logo.py            # writes rg35xx/SDRg35xx.png (144)
    python tools/make_logo.py --big      # also writes a 512 px preview
"""
import math
import random
import sys

from PIL import Image, ImageDraw, ImageFilter

SS = 4  # supersample factor


def lut(t):
    """Device waterfall palette (ui.initLUT stops)."""
    stops = [(0.00, (0, 0, 8)), (0.20, (24, 12, 80)), (0.45, (64, 80, 200)),
             (0.65, (210, 72, 60)), (0.82, (250, 200, 70)), (1.00, (255, 255, 255))]
    t = max(0.0, min(1.0, t))
    for (a, ca), (b, cb) in zip(stops, stops[1:]):
        if t <= b:
            f = (t - a) / (b - a)
            return tuple(round(ca[i] + f * (cb[i] - ca[i])) for i in range(3))
    return stops[-1][1]


def signal(x, row):
    """Synthetic spectrum value 0..1 at screen-x fraction x for a row.
    Three carriers (one strong, two weaker, one bursty) over noise."""
    rnd = random.Random(row * 7919 + int(x * 997))
    v = 0.10 + rnd.random() * 0.10
    v += 0.95 * math.exp(-((x - 0.50) / 0.050) ** 2)              # strong NFM carrier
    v += 0.62 * math.exp(-((x - 0.22) / 0.032) ** 2)              # weaker station
    if (row // 5) % 2 == 0:                                       # bursty (FT8-like)
        v += 0.70 * math.exp(-((x - 0.79) / 0.026) ** 2)
    return v


def shell_outline(box, r, big, u, steps=24):
    """Rounded-rect polygon (design units → px) with radius r on three
    corners and `big` on the bottom-right."""
    x0, y0, x1, y1 = box
    pts = []

    def corner(cx, cy, rad, a0):
        for i in range(steps + 1):
            a = math.radians(a0 + 90 * i / steps)
            pts.append((round((cx + rad * math.cos(a)) * u), round((cy + rad * math.sin(a)) * u)))

    corner(x0 + r, y0 + r, r, 180)        # top-left
    corner(x1 - r, y0 + r, r, 270)        # top-right
    corner(x1 - big, y1 - big, big, 0)    # bottom-right (large)
    corner(x0 + r, y1 - r, r, 90)         # bottom-left
    return pts


def render(size):
    S = size * SS
    img = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    u = S / 144.0  # design units: 144-unit canvas

    def R(*v):
        return [round(c * u) for c in v]

    # --- background tile: deep navy rounded square with subtle vertical gradient
    bg = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    grad = Image.new("RGBA", (S, S))
    gd = ImageDraw.Draw(grad)
    for y in range(S):
        f = y / S
        gd.line([(0, y), (S, y)], fill=(int(14 + 10 * f), int(20 + 12 * f), int(38 + 18 * f), 255))
    mask = Image.new("L", (S, S), 0)
    ImageDraw.Draw(mask).rounded_rectangle(R(2, 2, 142, 142), radius=round(28 * u), fill=255)
    bg.paste(grad, (0, 0), mask)
    img = Image.alpha_composite(img, bg)
    d = ImageDraw.Draw(img)

    # --- radio waves (behind the body): arcs from the antenna tip
    tip = (112, 22)
    for i, (r, a) in enumerate([(8, 235), (14, 175), (20, 115)]):
        box = R(tip[0] - r, tip[1] - r, tip[0] + r, tip[1] + r)
        d.arc(box, start=-70, end=10, fill=(53, 224, 138, a), width=max(1, round(2.8 * u)))
        d.arc(box, start=170, end=250, fill=(53, 224, 138, a), width=max(1, round(2.8 * u)))


    # --- body: vertical handheld with soft drop shadow
    body = (34, 28, 110, 132)
    shadow = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    ImageDraw.Draw(shadow).rounded_rectangle(R(body[0] + 2, body[1] + 4, body[2] + 2, body[3] + 4),
                                             radius=round(11 * u), fill=(0, 0, 0, 140))
    shadow = shadow.filter(ImageFilter.GaussianBlur(3 * u))
    img = Image.alpha_composite(img, shadow)
    d = ImageDraw.Draw(img)
    # shell: light grey like the RG35XX Plus "grey" colourway, with a
    # bigger rounded bottom-right corner (the Game Boy signature)
    shell = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    sd = ImageDraw.Draw(shell)
    # Game Boy signature: small radius on three corners, a big one
    # bottom-right — traced as one polygon so the join is seamless.
    sd.polygon(shell_outline(body, 9, 22, u), fill=(206, 210, 216, 255))
    # top highlight band
    hl = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    ImageDraw.Draw(hl).rounded_rectangle(R(body[0] + 1, body[1] + 1, body[2] - 1, body[1] + 10),
                                         radius=round(8 * u), fill=(255, 255, 255, 60))
    shell = Image.alpha_composite(shell, hl)
    img = Image.alpha_composite(img, shell)
    d = ImageDraw.Draw(img)

    # --- telescopic antenna, drawn over the shell so its foot is visible:
    # a dark mount block on the top edge, a two-section rod, a ball tip.
    base = (100, 29)
    d.rounded_rectangle(R(base[0] - 4, base[1] - 3, base[0] + 4, base[1] + 3), radius=round(1.5 * u), fill=(70, 76, 88, 255))
    mid = (base[0] + (tip[0] - base[0]) * 0.55, base[1] + (tip[1] - base[1]) * 0.55)
    d.line(R(*base, *mid), fill=(120, 128, 142, 255), width=round(4.4 * u))
    d.line(R(*mid, *tip), fill=(175, 183, 196, 255), width=round(3.0 * u))
    d.ellipse(R(tip[0] - 3.4, tip[1] - 3.4, tip[0] + 3.4, tip[1] + 3.4), fill=(225, 230, 238, 255))

    # --- screen bezel + display
    bez = (40, 34, 104, 82)
    d.rounded_rectangle(R(*bez), radius=round(5 * u), fill=(40, 44, 54, 255))
    scr = (44, 38, 100, 78)
    sx0, sy0, sx1, sy1 = R(*scr)
    sw, sh = sx1 - sx0, sy1 - sy0
    disp = Image.new("RGBA", (sw, sh), (0, 0, 8, 255))
    px = disp.load()
    split = int(sh * 0.36)  # spectrum line on top, waterfall below
    for y in range(split, sh):
        row = (y - split) // max(1, SS)
        for x in range(sw):
            px[x, y] = lut(signal(x / sw, row) / 1.15) + (255,)
    dd = ImageDraw.Draw(disp)
    pts = []
    for x in range(0, sw, max(1, SS // 2)):
        v = min(1.0, signal(x / sw, 999) / 1.15)
        pts.append((x, split - 2 - v * (split - 6)))
    dd.line(pts, fill=(53, 224, 138, 255), width=max(1, round(1.4 * u)))
    # amber channel brackets around the strong carrier (app's VFO marker)
    cx, bw = int(sw * 0.50), int(sw * 0.13)
    am = (234, 179, 8, 255)
    lw = max(1, round(1.2 * u))
    for xx in (cx - bw // 2, cx + bw // 2):
        dd.line([(xx, 1), (xx, sh - 1)], fill=(234, 179, 8, 110), width=lw)
    for xx, sgn in ((cx - bw // 2, 1), (cx + bw // 2, -1)):
        dd.line([(xx, 1), (xx + sgn * round(3 * u), 1)], fill=am, width=lw)
        dd.line([(xx, sh - 2), (xx + sgn * round(3 * u), sh - 2)], fill=am, width=lw)
    smask = Image.new("L", (sw, sh), 0)
    ImageDraw.Draw(smask).rounded_rectangle([0, 0, sw - 1, sh - 1], radius=round(2 * u), fill=255)
    img.paste(disp, (sx0, sy0), smask)
    d = ImageDraw.Draw(img)

    # --- D-pad (left)
    dc = (52, 104)
    arm, th = 9, 4.2
    dpad = (52, 58, 66, 255)
    d.rounded_rectangle(R(dc[0] - arm, dc[1] - th, dc[0] + arm, dc[1] + th), radius=round(1.6 * u), fill=dpad)
    d.rounded_rectangle(R(dc[0] - th, dc[1] - arm, dc[0] + th, dc[1] + arm), radius=round(1.6 * u), fill=dpad)
    d.ellipse(R(dc[0] - 1.6, dc[1] - 1.6, dc[0] + 1.6, dc[1] + 1.6), fill=(36, 40, 48, 255))

    # --- ABXY diamond (right), RG35XX-style colours
    bc = (90, 103)
    off, br = 7.2, 3.9
    for (dx, dy), col in (((0, -1), (66, 133, 244)),   # X blue
                          ((-1, 0), (52, 168, 83)),    # Y green
                          ((1, 0), (234, 67, 53)),     # A red
                          ((0, 1), (250, 204, 21))):   # B yellow
        x, y = bc[0] + dx * off, bc[1] + dy * off
        d.ellipse(R(x - br, y - br + 0.7, x + br, y + br + 0.7), fill=(0, 0, 0, 80))
        d.ellipse(R(x - br, y - br, x + br, y + br), fill=col + (255,))
        d.ellipse(R(x - br * 0.5, y - br * 0.65, x + br * 0.2, y - br * 0.1), fill=(255, 255, 255, 90))

    # --- MENU: small round button between the D-pad and ABXY
    mc, mr = (71, 97), 2.6
    d.ellipse(R(mc[0] - mr, mc[1] - mr + 0.6, mc[0] + mr, mc[1] + mr + 0.6), fill=(0, 0, 0, 70))
    d.ellipse(R(mc[0] - mr, mc[1] - mr, mc[0] + mr, mc[1] + mr), fill=(96, 102, 114, 255))
    d.ellipse(R(mc[0] - mr * 0.55, mc[1] - mr * 0.7, mc[0] + mr * 0.1, mc[1] - mr * 0.15), fill=(255, 255, 255, 70))

    # --- SELECT / START: pills tilted 45 degrees (Game Boy style)
    pill = (120, 126, 136, 255)
    half, w = 3.4, 3.0
    for x, y in ((61, 121), (73, 121)):
        p0 = (x - half * 0.7071, y + half * 0.7071)
        p1 = (x + half * 0.7071, y - half * 0.7071)
        d.line(R(*p0, *p1), fill=pill, width=round(w * u))
        for q in (p0, p1):  # round caps
            d.ellipse(R(q[0] - w / 2, q[1] - w / 2, q[0] + w / 2, q[1] + w / 2), fill=pill)
    for i in range(3):
        x = 95 + i * 3.6
        d.line(R(x, 116 + i * 0.0, x - 3, 125), fill=(150, 156, 166, 255), width=round(1.3 * u))

    # clip everything (waves, shadow) to the tile so nothing bleeds
    # past the rounded corners in the launcher
    clipped = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    clipped.paste(img, (0, 0), mask)
    return clipped.resize((size, size), Image.LANCZOS)


if __name__ == "__main__":
    out = "rg35xx/SDRg35xx.png"
    im = render(144)
    im.convert("RGBA").save(out)
    print("wrote", out, im.size)
    if "--big" in sys.argv:
        render(512).save("dist/logo_preview_512.png")
        print("wrote dist/logo_preview_512.png")
