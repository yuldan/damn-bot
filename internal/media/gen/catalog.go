package gen

import (
	"fmt"
)

// ImageBackends is the closed image backend catalog (MEDIA-016..023).
func ImageBackends() []string {
	return []string{"codex", "deepinfra", "fal", "krea", "meta", "openai", "openrouter", "xai"}
}

// VideoBackends is the closed video backend catalog (MEDIA-024..028).
func VideoBackends() []string {
	return []string{"deepinfra", "fal", "openrouter", "xai"}
}

// NewImageBackend builds one image backend from the closed catalog. The key
// is a resolved secret; an empty key fails closed as module_unavailable.
func NewImageBackend(name string, doer Doer, endpoint, key string) (ImageGen, error) {
	switch name {
	case "openai":
		return NewOpenAIImages(doer, endpoint, key)
	case "fal":
		return NewFALImages(doer, endpoint, key)
	case "codex":
		return NewCodexImages(doer, endpoint, key)
	case "openrouter":
		return NewOpenRouterImages(doer, endpoint, key)
	case "xai":
		return NewXAIImages(doer, endpoint, key)
	case "deepinfra":
		return NewDeepInfraImages(doer, endpoint, key)
	case "krea":
		return NewKreaImages(doer, endpoint, key)
	case "meta":
		return NewMetaImages(doer, endpoint, key)
	}
	return nil, fmt.Errorf("%w: image backend %q", ErrCapability, name)
}

// NewVideoBackend builds one video backend from the closed catalog.
func NewVideoBackend(name string, doer Doer, createURL, statusURL, key string) (VideoGen, error) {
	switch name {
	case "fal":
		return NewFALVideo(doer, createURL, statusURL, key)
	case "xai":
		return NewXAIVideo(doer, createURL, statusURL, key)
	case "openrouter":
		return NewOpenRouterVideo(doer, createURL, statusURL, key)
	case "deepinfra":
		return NewDeepInfraVideo(doer, createURL, statusURL, key)
	}
	return nil, fmt.Errorf("%w: video backend %q", ErrCapability, name)
}
