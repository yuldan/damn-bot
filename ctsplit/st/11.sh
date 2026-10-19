python3 - <<'PY'
m = open('go.mod').read()
m = m.replace('\tgolang.org/x/sys v0.48.0\n', '\tgolang.org/x/sys v0.48.0\n\tgolang.org/x/text v0.43.0\n', 1)
open('go.mod', 'w').write(m)
PY
git show b2fec95:go.sum > go.sum
python3 $S/gen.py decisions 1,2,3,4,5,6,7,8,9,10,12,14,15,16 0
python3 $S/gen.py changelog $S/st/$K.cl
