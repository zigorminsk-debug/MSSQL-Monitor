# -*- coding: utf-8 -*-
# PyInstaller spec: MS SQL Monitor (одна папка, без консоли)
import os

block_cipher = None
here = os.path.abspath('.')

datas = []
icon = os.path.join(here, 'icon.ico')
icon_arg = [('icon.ico', '.')] if os.path.exists(icon) else []

hidden = ['pyodbc']
try:
    import pymssql  # noqa
    hidden.append('pymssql')
except Exception:
    pass

a = Analysis(
    ['main.py'],
    pathex=[here],
    binaries=[],
    datas=icon_arg,
    hiddenimports=hidden,
    hookspath=[],
    runtime_hooks=[],
    excludes=['pytest', 'setuptools', 'tkinter.test'],
    cipher=block_cipher,
)
pyz = PYZ(a.pure, a.zipped_data, cipher=block_cipher)

exe = EXE(
    pyz,
    a.scripts,
    exclude_binaries=True,
    name='MSSQLMonitor',
    debug=False,
    strip=False,
    upx=False,
    console=False,
    icon=icon if os.path.exists(icon) else None,
)
coll = COLLECT(
    exe,
    a.binaries,
    a.zipfiles,
    a.datas,
    strip=False,
    upx=False,
    name='MSSQLMonitor',
)
