//go:build windows

package registrybackup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/sys/windows/registry"
)

func backup(ctx context.Context, roots []string) ([]byte, error) {
	document := Document{Version: 1}
	for _, rootPath := range roots {
		root, relative, err := splitRoot(rootPath)
		if err != nil {
			return nil, err
		}
		key, err := registry.OpenKey(root, relative, registry.READ)
		if errors.Is(err, registry.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("open registry key %q: %w", rootPath, err)
		}
		walkErr := readKey(ctx, key, rootPath, &document)
		closeErr := key.Close()
		if err := errors.Join(walkErr, closeErr); err != nil {
			return nil, fmt.Errorf("back up registry key %q: %w", rootPath, err)
		}
	}
	return json.Marshal(document)
}

func readKey(ctx context.Context, key registry.Key, fullPath string, document *Document) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	names, err := key.ReadValueNames(-1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	data := KeyData{Path: fullPath}
	for _, name := range names {
		value, err := readValue(key, name)
		if err != nil {
			return fmt.Errorf("read registry value %q: %w", name, err)
		}
		data.Values = append(data.Values, value)
	}
	document.Keys = append(document.Keys, data)
	subkeys, err := key.ReadSubKeyNames(-1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	for _, name := range subkeys {
		subkey, err := registry.OpenKey(key, name, registry.READ)
		if err != nil {
			return err
		}
		walkErr := readKey(ctx, subkey, fullPath+"/"+name, document)
		closeErr := subkey.Close()
		if err := errors.Join(walkErr, closeErr); err != nil {
			return err
		}
	}
	return nil
}

func readValue(key registry.Key, name string) (ValueData, error) {
	_, valueType, err := key.GetValue(name, nil)
	if err != nil {
		return ValueData{}, err
	}
	value := ValueData{Name: name, Type: valueType}
	switch valueType {
	case registry.SZ, registry.EXPAND_SZ:
		content, _, err := key.GetStringValue(name)
		value.String = &content
		return value, err
	case registry.MULTI_SZ:
		content, _, err := key.GetStringsValue(name)
		value.Strings = content
		return value, err
	case registry.DWORD, registry.QWORD:
		content, _, err := key.GetIntegerValue(name)
		value.Integer = &content
		return value, err
	case registry.BINARY:
		content, _, err := key.GetBinaryValue(name)
		value.Binary = content
		return value, err
	default:
		return ValueData{}, fmt.Errorf("unsupported registry value type %d", valueType)
	}
}

func restore(ctx context.Context, data []byte) error {
	var document Document
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("decode registry backup: %w", err)
	}
	if document.Version != 1 {
		return fmt.Errorf("unsupported registry backup version %d", document.Version)
	}
	for _, keyData := range document.Keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		root, relative, err := splitRoot(keyData.Path)
		if err != nil {
			return err
		}
		key, _, err := registry.CreateKey(root, relative, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("create registry key %q: %w", keyData.Path, err)
		}
		for _, value := range keyData.Values {
			if err := writeValue(key, value); err != nil {
				return fmt.Errorf("restore registry value %q in %q: %w", value.Name, keyData.Path, errors.Join(err, key.Close()))
			}
		}
		if err := key.Close(); err != nil {
			return err
		}
	}
	return nil
}

func writeValue(key registry.Key, value ValueData) error {
	switch value.Type {
	case registry.SZ:
		if value.String == nil {
			return errors.New("missing string registry value")
		}
		return key.SetStringValue(value.Name, *value.String)
	case registry.EXPAND_SZ:
		if value.String == nil {
			return errors.New("missing expand-string registry value")
		}
		return key.SetExpandStringValue(value.Name, *value.String)
	case registry.MULTI_SZ:
		return key.SetStringsValue(value.Name, value.Strings)
	case registry.DWORD:
		if value.Integer == nil {
			return errors.New("missing DWORD registry value")
		}
		return key.SetDWordValue(value.Name, uint32(*value.Integer))
	case registry.QWORD:
		if value.Integer == nil {
			return errors.New("missing QWORD registry value")
		}
		return key.SetQWordValue(value.Name, *value.Integer)
	case registry.BINARY:
		return key.SetBinaryValue(value.Name, value.Binary)
	default:
		return fmt.Errorf("unsupported registry value type %d", value.Type)
	}
}

func splitRoot(fullPath string) (registry.Key, string, error) {
	normalized := strings.ReplaceAll(fullPath, `\`, "/")
	parts := strings.SplitN(normalized, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return 0, "", fmt.Errorf("invalid registry path %q", fullPath)
	}
	switch strings.ToUpper(parts[0]) {
	case "HKEY_CURRENT_USER", "HKCU":
		return registry.CURRENT_USER, strings.ReplaceAll(parts[1], "/", `\`), nil
	case "HKEY_LOCAL_MACHINE", "HKLM":
		return registry.LOCAL_MACHINE, strings.ReplaceAll(parts[1], "/", `\`), nil
	default:
		return 0, "", fmt.Errorf("unsupported registry hive in %q", fullPath)
	}
}
