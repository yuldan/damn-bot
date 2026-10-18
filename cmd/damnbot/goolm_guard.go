//go:build !goolm

// Guard E-39 / M-04: build tanpa tag `goolm` sengaja gagal compile.
// Selalu build lewat Makefile (TAGS ?= goolm) atau `-tags goolm`.
package main

var _ = buildRequiresTagGoolm // undefined: build tanpa -tags goolm dilarang (E-39)
