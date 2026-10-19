git checkout b2fec95 -- internal/archtest/allowed.go
python3 $S/gen.py hunks docs/ENGINEERING.md 0
python3 $S/gen.py decisions 1,2,5,6,10,15 0
python3 $S/gen.py changelog $S/st/$K.cl
