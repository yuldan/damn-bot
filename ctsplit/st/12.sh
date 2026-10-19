git checkout b2fec95 -- .gitattributes internal/codetools/testdata docs/TRUST-MODEL.md loc.budget
python3 $S/gen.py hunks docs/CODE-TOOLS.md 0
python3 $S/gen.py hunks docs/ENGINEERING.md 0,1
python3 $S/gen.py decisions 1,2,3,4,5,6,7,8,9,10,12,13,14,15,16 1
python3 $S/gen.py changelog $S/st/$K.cl
