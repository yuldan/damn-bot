#!/usr/bin/env python3
"""Stage asset generator. Run inside the worktree.

gen.py decisions <butir,...> <adr105:0|1>
gen.py changelog <file with lines to insert>
gen.py hunks <path> <idx,...>   (apply hunks of 8560b32..678ab3e diff; cumulative)
"""
import re, subprocess, sys

BASE, FINAL = '339d92a', 'b2fec95'
D = '/tmp/ctsplit/'


def decisions(butir, adr105):
    base = open(D + 'DECISIONS.base').read()
    final = open(D + 'DECISIONS.final').read()
    tail = final[len(base):].split('\n')
    keep = set(int(x) for x in butir.split(',') if x)
    out = []
    for i, l in enumerate(tail):
        m = re.match(r'^(\d+)\. ', l)
        if 6 <= i <= 21 and m and int(m.group(1)) not in keep:
            continue
        if i >= 24 and adr105 == '0':
            break
        out.append(l)
    s = base + '\n'.join(out)
    if adr105 == '0':
        s = s.rstrip('\n') + '\n'
    open('docs/DECISIONS.md', 'w').write(s)


def changelog(path):
    base = open(D + 'CHANGELOG.base').read().split('\n')
    ins = [l for l in open(path).read().split('\n') if l]
    out = base[:20] + ins + base[20:]
    open('CHANGELOG.md', 'w').write('\n'.join(out))


def hunks(path, idx):
    diff = subprocess.run(['git', 'diff', BASE, FINAL, '--', path], capture_output=True, text=True, check=True).stdout
    parts = re.split(r'(?m)^(?=@@ )', diff)
    head, hs = parts[0], parts[1:]
    sel = [hs[int(i)] for i in idx.split(',') if i]
    subprocess.run(['git', 'show', f'{BASE}:{path}'], stdout=open(path, 'w'), check=True)
    if sel:
        subprocess.run(['git', 'apply', '--recount', '-'], input=head + ''.join(sel), text=True, check=True)


if __name__ == '__main__':
    cmd = sys.argv[1]
    if cmd == 'decisions':
        decisions(sys.argv[2], sys.argv[3])
    elif cmd == 'changelog':
        changelog(sys.argv[2])
    elif cmd == 'hunks':
        hunks(sys.argv[2], sys.argv[3])
