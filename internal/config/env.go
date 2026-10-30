package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// applyEnv applies DAMNBOT_<SECTION>_<KEY> overrides derived from the struct
// tags (GAP-OPS-01). Only scalar fields are overridable; maps, slices and
// nested messages are file-only. The variable name is the single alias.
func (l *Loader) applyEnv(cfg *Config) error {
	v := reflect.ValueOf(cfg).Elem()
	t := v.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		section := tomlTagOf(&field)
		if section == "" || v.Field(i).Kind() != reflect.Struct {
			continue
		}
		fv, ft := v.Field(i), v.Field(i).Type()
		for j := range ft.NumField() {
			sf := ft.Field(j)
			key := tomlTagOf(&sf)
			field := fv.Field(j)
			if key == "" || !field.CanSet() {
				continue
			}
			env := EnvName(section, key)
			raw := l.deps.Getenv(env)
			if raw == "" {
				continue
			}
			if err := setEnvValue(field, raw); err != nil {
				return &Error{
					Kind: ErrInvalidValue, Key: section + "." + key,
					Msg: fmt.Sprintf("%s: %v", env, err),
				}
			}
		}
	}
	return cfg.Validate()
}

// EnvName is the one generated name of a config key (no aliases).
func EnvName(section, key string) string {
	return "DAMNBOT_" + strings.ToUpper(section) + "_" + strings.ToUpper(key)
}

func tomlTagOf(f *reflect.StructField) string {
	tag, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
	if tag == "-" {
		return ""
	}
	return tag
}

func setEnvValue(v reflect.Value, raw string) error {
	if v.Type() == reflect.TypeFor[Duration]() {
		var d Duration
		if err := d.UnmarshalText([]byte(raw)); err != nil {
			return err
		}
		v.Set(reflect.ValueOf(d))
		return nil
	}
	kind := v.Kind()
	if kind == reflect.String {
		v.SetString(raw)
		return nil
	}
	if kind == reflect.Bool {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("want bool")
		}
		v.SetBool(b)
		return nil
	}
	if kind >= reflect.Int && kind <= reflect.Int64 {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("want integer")
		}
		v.SetInt(n)
		return nil
	}
	if kind == reflect.Float32 || kind == reflect.Float64 {
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("want number")
		}
		v.SetFloat(f)
		return nil
	}
	return fmt.Errorf("type %s is not env-overridable", v.Type())
}
