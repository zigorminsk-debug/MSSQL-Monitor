# -*- coding: utf-8 -*-
"""Генерация иконки приложения (icon.ico) с помощью Pillow."""
import os

def main():
    try:
        from PIL import Image, ImageDraw
    except ImportError:
        print('Pillow не установлен — пропускаю иконку')
        return 1

    S = 256
    img = Image.new('RGBA', (S, S), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    # тёмный скруглённый фон
    d.rounded_rectangle([8, 8, S - 8, S - 8], radius=52, fill=(15, 23, 42, 255))

    cx, cy, r = S // 2, S // 2 + 16, 74
    # дуга шкалы (спидометр)
    d.arc([cx - r, cy - r, cx + r, cy + r], start=150, end=390, fill=(148, 163, 184, 255), width=14)
    d.arc([cx - r, cy - r, cx + r, cy + r], start=150, end=330, fill=(37, 99, 235, 255), width=14)
    # деления
    import math
    for ang in (150, 210, 270, 330):
        a = math.radians(ang)
        x1, y1 = cx + (r - 24) * math.cos(a), cy - (r - 24) * math.sin(a)
        x2, y2 = cx + (r - 36) * math.cos(a), cy - (r - 36) * math.sin(a)
        d.line([x1, y1, x2, y2], fill=(203, 213, 225, 255), width=6)
    # стрелка
    a = math.radians(300)
    d.line([cx, cy, cx + (r - 30) * math.cos(a), cy - (r - 30) * math.sin(a)],
           fill=(239, 68, 68, 255), width=10)
    d.ellipse([cx - 12, cy - 12, cx + 12, cy + 12], fill=(239, 68, 68, 255))
    # точка-индикатор
    d.ellipse([cx + 44, cy - 74, cx + 62, cy - 56], fill=(34, 197, 94, 255))

    out = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'icon.ico')
    img.save(out, sizes=[(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)])
    img.convert('RGB').save(os.path.join(os.path.dirname(os.path.abspath(__file__)), 'icon.png'))
    print('Иконка сохранена:', out)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
