#!/usr/bin/env python3
"""Render exported production TUI ANSI frames to a fixed-cell review image.

Developer-only: needs Pillow, regex, wcwidth and a Chinese font. Does not launch
a terminal, read user configuration or execute any command in the frame.
"""
import argparse
from pathlib import Path
import re

from PIL import Image, ImageDraw, ImageFont
import regex
from wcwidth import wcswidth


def render(path: Path, target: Path) -> None:
    theme = "light" if path.name.startswith("light") else "dark"
    bg = (247, 248, 252) if theme == "light" else (26, 27, 38)
    fg = (31, 36, 48) if theme == "light" else (192, 202, 245)
    text = path.read_text()
    size = re.search(r"(\d+)x(\d+)", path.name)
    cols, rows = map(int, size.groups())
    cell, row_h, inset = 12, 27, 24
    canvas = Image.new("RGB", (cols * cell + inset * 2, rows * row_h + inset * 2), bg)
    draw = ImageDraw.Draw(canvas)
    mono = ImageFont.truetype("/System/Library/Fonts/Menlo.ttc", 20)
    cjk = ImageFont.truetype("/System/Library/Fonts/STHeiti Medium.ttc", 20)
    emoji = ImageFont.truetype("/System/Library/Fonts/Apple Color Emoji.ttc", 20)
    sequence = re.compile(r"(\x1b\[[\d;]*m)")
    state = {"fg": fg, "bg": bg, "bold": False}
    for row, line in enumerate(text.split("\n")):
        col = 0
        for token in sequence.split(line):
            if token.startswith("\x1b["):
                codes = [int(n or 0) for n in token[2:-1].split(";")]
                i = 0
                while i < len(codes):
                    n = codes[i]
                    if n == 0:
                        state = {"fg": fg, "bg": bg, "bold": False}
                    elif n == 1:
                        state["bold"] = True
                    elif n == 22:
                        state["bold"] = False
                    elif n == 39:
                        state["fg"] = fg
                    elif n == 49:
                        state["bg"] = bg
                    elif n in (38, 48) and i + 4 < len(codes) and codes[i + 1] == 2:
                        state["fg" if n == 38 else "bg"] = tuple(codes[i + 2:i + 5])
                        i += 4
                    i += 1
                continue
            for cluster in regex.findall(r"\X", token):
                width = max(0, wcswidth(cluster))
                if not width:
                    continue
                x, y = inset + col * cell, inset + row * row_h
                draw.rectangle((x, y, x + width * cell - 1, y + row_h - 1), fill=state["bg"])
                is_emoji = any(0x1F000 <= ord(r) <= 0x1FAFF or ord(r) in (0x2699, 0x26A0) for r in cluster)
                font = emoji if is_emoji else cjk if any(ord(r) > 0x2E80 for r in cluster) else mono
                kwargs = {"embedded_color": True} if is_emoji else {}
                draw.text((x, y + 2), cluster, font=font, fill=state["fg"], stroke_width=0 if is_emoji or not state["bold"] else 1, **kwargs)
                col += width
    canvas.save(target)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("frame", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    render(args.frame, args.output)
