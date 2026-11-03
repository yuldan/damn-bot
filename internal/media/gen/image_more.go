package gen

import (
	"encoding/json"
	"fmt"
)

func newKeyedImage(name, model, endpoint, key string, doer Doer, price Price,
	encode func(*ImageRequest) any, decode func([]byte, *ImageRequest) (ImageResult, error),
) (ImageGen, error) {
	if key == "" || doer == nil {
		return nil, fmt.Errorf("%w: %s images needs a key and egress client", ErrModuleUnavailable, name)
	}
	return &httpImage{
		name: name, model: model, doer: doer, endpoint: endpoint, auth: "Bearer " + key,
		price: price, encode: encode, decode: decode,
	}, nil
}

func imageDims(size string) (int, int) {
	w, h, err := splitSize(size)
	if err != nil {
		return 0, 0
	}
	return w, h
}

func openAIImagePayload(req *ImageRequest) any {
	return map[string]any{"model": req.Model, "prompt": req.Prompt, "size": req.Size, "n": 1}
}

// NewCodexImages is OpenAI Images through an OAuth token (no API key print).
func NewCodexImages(doer Doer, endpoint, token string) (ImageGen, error) {
	if endpoint == "" {
		endpoint = openAIEndpoint
	}
	return newKeyedImage("codex", "gpt-image-1", endpoint, token, doer,
		imagePrice(5_000_000, 40_000_000, map[string]int64{
			"1024x1024": 40_000, "1024x1536": 60_000, "1536x1024": 60_000,
		}), openAIImagePayload, decodeOpenAI)
}

// NewOpenRouterImages uses OpenRouter's chat-completions image response.
func NewOpenRouterImages(doer Doer, endpoint, key string) (ImageGen, error) {
	if endpoint == "" {
		endpoint = "https://openrouter.ai/api/v1/chat/completions"
	}
	return newKeyedImage("openrouter", "google/gemini-2.5-flash-image", endpoint, key, doer,
		imagePrice(0, 0, map[string]int64{"1024x1024": 30_000}),
		func(req *ImageRequest) any {
			return map[string]any{
				"model":    req.Model,
				"messages": []map[string]string{{"role": "user", "content": req.Prompt}},
			}
		}, decodeOpenRouter)
}

func decodeOpenRouter(data []byte, _ *ImageRequest) (ImageResult, error) {
	var out struct {
		Choices []struct {
			Message struct {
				Images []struct {
					URL      string `json:"url"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"images"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 || len(out.Choices[0].Message.Images) == 0 {
		return ImageResult{}, fmt.Errorf("%w: openrouter response", ErrInvalidRequest)
	}
	img := out.Choices[0].Message.Images[0]
	if img.URL == "" {
		img.URL = img.ImageURL.URL
	}
	return ImageResult{URL: img.URL, MIME: "image/png"}, nil
}

// NewXAIImages is xAI's grok-imagine-image backend.
func NewXAIImages(doer Doer, endpoint, key string) (ImageGen, error) {
	if endpoint == "" {
		endpoint = "https://api.x.ai/v1/images/generations"
	}
	return newKeyedImage("xai", "grok-imagine-image", endpoint, key, doer,
		imagePrice(0, 0, map[string]int64{"1024x1024": 35_000, "1024x1536": 50_000}),
		openAIImagePayload, decodeOpenAI)
}

// NewDeepInfraImages is DeepInfra's OpenAI-compatible image endpoint.
func NewDeepInfraImages(doer Doer, endpoint, key string) (ImageGen, error) {
	if endpoint == "" {
		endpoint = "https://api.deepinfra.com/v1/openai/images/generations"
	}
	return newKeyedImage("deepinfra", "black-forest-labs/FLUX-1-schnell", endpoint, key, doer,
		imagePrice(0, 0, map[string]int64{"1024x1024": 12_000, "1024x1536": 18_000}),
		openAIImagePayload, decodeOpenAI)
}

// NewKreaImages is Krea 2.
func NewKreaImages(doer Doer, endpoint, key string) (ImageGen, error) {
	if endpoint == "" {
		endpoint = "https://api.krea.ai/api/v1/generate"
	}
	return newKeyedImage("krea", "krea-2", endpoint, key, doer,
		imagePrice(0, 0, map[string]int64{"1024x1024": 20_000, "1024x1536": 30_000}),
		func(req *ImageRequest) any {
			w, h := imageDims(req.Size)
			return map[string]any{"model": req.Model, "prompt": req.Prompt, "width": w, "height": h}
		}, decodeKrea)
}

func decodeKrea(data []byte, _ *ImageRequest) (ImageResult, error) {
	var out struct {
		URL    string `json:"url"`
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return ImageResult{}, fmt.Errorf("%w: krea response", ErrInvalidRequest)
	}
	if out.URL == "" && len(out.Images) > 0 {
		out.URL = out.Images[0].URL
	}
	if out.URL == "" {
		return ImageResult{}, fmt.Errorf("%w: krea response", ErrInvalidRequest)
	}
	return ImageResult{URL: out.URL, MIME: "image/png"}, nil
}

// NewMetaImages is Meta AI muse-image; the key arrives from the secret store.
func NewMetaImages(doer Doer, endpoint, key string) (ImageGen, error) {
	if endpoint == "" {
		endpoint = "https://api.meta.ai/v1/images/generations"
	}
	return newKeyedImage("meta", "muse-image", endpoint, key, doer,
		imagePrice(0, 0, map[string]int64{"1024x1024": 25_000}),
		openAIImagePayload, decodeOpenAI)
}
