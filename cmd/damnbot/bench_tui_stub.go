//go:build !bench

package main

import "github.com/damnbot/damnbot/internal/config"

func benchTUIOverrides(*runOptions, *config.Config) {}

func benchTrace(string) {}
