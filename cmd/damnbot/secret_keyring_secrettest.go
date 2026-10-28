//go:build secrettest

package main

import (
	"errors"
	"os"
	"sync"

	"github.com/damnbot/damnbot/internal/secret"
)

type memKeyring struct {
	mu sync.Mutex
	m  map[string]string
}

func (k *memKeyring) Get(service, user string) (string, error) {
	if os.Getenv("DAMNBOT_TEST_KEYRING") == "fail" {
		return "", errors.New("test keyring unavailable")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if v, ok := k.m[service+"/"+user]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}

func (k *memKeyring) Set(service, user, value string) error {
	if os.Getenv("DAMNBOT_TEST_KEYRING") == "fail" {
		return errors.New("test keyring unavailable")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[service+"/"+user] = value
	return nil
}

func (k *memKeyring) Delete(service, user string) error {
	if os.Getenv("DAMNBOT_TEST_KEYRING") == "fail" {
		return errors.New("test keyring unavailable")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, service+"/"+user)
	return nil
}

var testKeyring = &memKeyring{m: map[string]string{}}

func setupKeyring() secret.Keyring { return testKeyring }
