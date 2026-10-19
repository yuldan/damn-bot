python3 - <<'PY'
m = open('go.mod').read()
m = m.replace('require (\n', 'require (\n\tgithub.com/bmatcuk/doublestar/v4 v4.10.3\n', 1)
open('go.mod', 'w').write(m)
s = open('go.sum').read()
s = ('github.com/bmatcuk/doublestar/v4 v4.10.3 h1:1UIv1+fqH3++ViVkpnaPTB+Pi1UHycnPFAq14zgK+gI=\n'
     'github.com/bmatcuk/doublestar/v4 v4.10.3/go.mod h1:xBQ8jztBU6kakFMg+8WGxn0c6z1fTSPVIjEY1Wr7jzc=\n') + s
open('go.sum', 'w').write(s)
PY
python3 $S/gen.py decisions 1,2,3,4,5,6,10,15 0
python3 $S/gen.py changelog $S/st/$K.cl
