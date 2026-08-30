package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v2"
)

func (c *Config) GetField(path string) (any, error) {
	v, err := resolvePath(reflect.ValueOf(c).Elem(), splitPath(path))
	if err != nil {
		return nil, err
	}
	return v.Interface(), nil
}

func (c *Config) SetField(path, value string) error {
	v, err := resolvePath(reflect.ValueOf(c).Elem(), splitPath(path))
	if err != nil {
		return err
	}
	if !v.CanSet() {
		return fmt.Errorf("field %q is not settable", path)
	}
	return assignScalar(v, value)
}

func (c *Config) DeleteField(path string) error {
	v, err := resolvePath(reflect.ValueOf(c).Elem(), splitPath(path))
	if err != nil {
		return err
	}
	if !v.CanSet() {
		return fmt.Errorf("field %q is not settable", path)
	}
	v.Set(reflect.Zero(v.Type()))
	return nil
}

func (c *Config) Save() error {
	if c.SourcePath == "" {
		return fmt.Errorf("no source path recorded for config")
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if existing, err := os.ReadFile(c.SourcePath); err == nil {
		_ = os.WriteFile(c.SourcePath+".backup", existing, 0o600)
	}
	return os.WriteFile(c.SourcePath, data, 0o600)
}

func (c *Config) RawYAML() (string, error) {
	if c.SourcePath == "" {
		return "", fmt.Errorf("no source path recorded for config")
	}
	data, err := os.ReadFile(c.SourcePath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (c *Config) ApplyDeltas(deltas map[string]string) error {
	var firstErr error
	for path, value := range deltas {
		if err := c.SetField(path, value); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("set %q: %w", path, err)
		}
	}
	return firstErr
}

func splitPath(path string) []string {
	return strings.Split(path, ".")
}

func resolvePath(v reflect.Value, segs []string) (reflect.Value, error) {
	for i, seg := range segs {
		for v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return reflect.Value{}, fmt.Errorf("nil pointer before %q", seg)
			}
			v = v.Elem()
		}
		switch v.Kind() {
		case reflect.Struct:
			f, err := fieldByYAML(v, seg)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("at %q: %w", strings.Join(segs[:i+1], "."), err)
			}
			v = f
		case reflect.Map:
			if v.Type().Key().Kind() != reflect.String {
				return reflect.Value{}, fmt.Errorf("cannot index non-string map at %q", seg)
			}
			mv := v.MapIndex(reflect.ValueOf(seg))
			if !mv.IsValid() {
				return reflect.Value{}, fmt.Errorf("key %q not found", seg)
			}
			v = mv
		default:
			return reflect.Value{}, fmt.Errorf("cannot descend into %s at %q", v.Kind(), seg)
		}
	}
	return v, nil
}

func fieldByYAML(v reflect.Value, name string) (reflect.Value, error) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tagName := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if tagName == "" {
			tagName = strings.ToLower(field.Name)
		}
		if tagName == "-" {
			continue
		}
		if tagName == name {
			return v.Field(i), nil
		}
	}
	return reflect.Value{}, fmt.Errorf("unknown field %q", name)
}

func assignScalar(v reflect.Value, s string) error {
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		v.SetFloat(f)
	case reflect.Ptr:
		nv := reflect.New(v.Type().Elem())
		if err := assignScalar(nv.Elem(), s); err != nil {
			return err
		}
		v.Set(nv)
	default:
		return fmt.Errorf("unsupported field kind %s", v.Kind())
	}
	return nil
}
