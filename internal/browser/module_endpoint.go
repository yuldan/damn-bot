//go:build !minimal

package browser

import (
	"fmt"
	"net/url"
	"path/filepath"
)

func validateEndpoints(cfg *ModuleConfig) error {
	if err := validateCDPEndpoint(cfg.CDPURL); err != nil {
		return err
	}
	if err := validateHTTPEndpoint(cfg.CamofoxURL, true, "camofox_url"); err != nil {
		return err
	}
	if err := validateHTTPEndpoint(cfg.CloudURL, false, "cloud_url"); err != nil {
		return err
	}
	if err := validateAbsPath(cfg.LightpandaBin, "lightpanda_bin", false); err != nil {
		return err
	}
	if err := validateAbsPath(cfg.UseBin, "use_bin", true); err != nil {
		return err
	}
	return validateAbsPath(cfg.ExtensionManifest, "extension_manifest", true)
}

func validateCDPEndpoint(raw string) error {
	if raw == "" {
		return nil
	}
	parsed, err := endpointURL(raw)
	if err != nil {
		return err
	}
	u, perr := url.Parse(parsed)
	if perr != nil || !localHost(u.Hostname()) {
		return fmt.Errorf("%w: cdp_url must be loopback", errBadConfig)
	}
	return nil
}

func validateHTTPEndpoint(raw string, loopback bool, name string) error {
	if raw == "" {
		return nil
	}
	if err := httpEndpoint(raw, loopback); err != nil {
		return fmt.Errorf("%w: %s", err, name)
	}
	return nil
}

func validateAbsPath(raw, name string, bounded bool) error {
	if raw == "" {
		return nil
	}
	if bounded && (!filepath.IsAbs(raw) || len(raw) > 1024) {
		return fmt.Errorf("%w: %s must be an absolute path of at most 1024 bytes", errBadConfig, name)
	}
	if !bounded && !filepath.IsAbs(raw) {
		return fmt.Errorf("%w: %s must be absolute", errBadConfig, name)
	}
	return nil
}
