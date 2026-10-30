//go:build secrettest

package main

import (
	"errors"
	configos "github.com/damnbot/damnbot/internal/config"
	"sync"

	"github.com/damnbot/damnbot/internal/secret"
	"github.com/zalando/go-keyring"
)

type memKeyring struct {
	mu sync.Mutex
	m  map[string]string
}

func (k *memKeyring) Get(service, user string) (string, error) {
	if configos.OS().Getenv("DAMNBOT_TEST_KEYRING") == "fail" {
		return "", errors.New("test keyring unavailable")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[service+"/"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (k *memKeyring) Set(service, user, value string) error {
	if configos.OS().Getenv("DAMNBOT_TEST_KEYRING") == "fail" {
		return errors.New("test keyring unavailable")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[service+"/"+user] = value
	return nil
}

func (k *memKeyring) Delete(service, user string) error {
	if configos.OS().Getenv("DAMNBOT_TEST_KEYRING") == "fail" {
		return errors.New("test keyring unavailable")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, service+"/"+user)
	return nil
}

var testKeyring = &memKeyring{m: map[string]string{}}

func setupKeyring() secret.Keyring { return testKeyring }
