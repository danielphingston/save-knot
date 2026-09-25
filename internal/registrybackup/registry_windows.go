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
	if err := deduplicateKeys(&document); err != nil {
		return nil, err
	}
	return json.Marshal(document)
}

func deduplicateKeys(document *Document) error {
	seen := make(map[string]struct{}, len(document.Keys))
	unique := document.Keys[:0]
	for _, key := range document.Keys {
		normalized, err := normalizePath(key.Path)
		if err != nil {
			return fmt.Errorf("invalid registry key path %q: %w", key.Path, err)
		}
		folded := strings.ToLower(normalized)
		if _, exists := seen[folded]; exists {
			continue
		}
		seen[folded] = struct{}{}
		unique = append(unique, key)
	}
	document.Keys = unique
	return nil
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

func restore(ctx context.Context, data []byte, roots []string) error {
	var document Document
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("decode registry backup: %w", err)
	}
	if document.Version != 1 {
		return fmt.Errorf("unsupported registry backup version %d", document.Version)
	}
	if err := validateDocument(document, roots); err != nil {
		return err
	}
	before, err := backup(ctx, roots)
	if err != nil {
		return fmt.Errorf("capture registry state before restore: %w", err)
	}
	if err := applyDocument(ctx, document, roots); err != nil {
		var previous Document
		decodeErr := json.Unmarshal(before, &previous)
		if decodeErr != nil {
			return errors.Join(err, fmt.Errorf("decode captured registry state: %w", decodeErr))
		}
		rollbackErr := applyDocument(context.WithoutCancel(ctx), previous, roots)
		return errors.Join(err, wrapError("roll back registry restore", rollbackErr))
	}
	return nil
}

func applyDocument(ctx context.Context, document Document, roots []string) error {
	for _, rootPath := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		root, relative, err := splitRoot(rootPath)
		if err != nil {
			return fmt.Errorf("invalid configured registry root %q: %w", rootPath, err)
		}
		rootNormalized, err := normalizePath(rootPath)
		if err != nil {
			return err
		}
		desired := newKeyNode("")
		for index := range document.Keys {
			keyData := &document.Keys[index]
			keyNormalized, err := normalizePath(keyData.Path)
			if err != nil {
				return err
			}
			if !strings.EqualFold(keyNormalized, rootNormalized) && !strings.HasPrefix(strings.ToLower(keyNormalized), strings.ToLower(rootNormalized)+"/") {
				continue
			}
			relativeKey := strings.TrimPrefix(keyNormalized[len(rootNormalized):], "/")
			insertKeyData(desired, relativeKey, keyData)
		}
		if desired.data == nil && len(desired.children) == 0 {
			if err := deleteTree(ctx, root, relative); err != nil {
				return fmt.Errorf("remove absent registry root %q: %w", rootPath, err)
			}
			continue
		}
		if err := reconcileKey(ctx, root, relative, desired); err != nil {
			return fmt.Errorf("reconcile registry root %q: %w", rootPath, err)
		}
	}
	return nil
}

type keyNode struct {
	name     string
	data     *KeyData
	children map[string]*keyNode
}

func newKeyNode(name string) *keyNode {
	return &keyNode{name: name, children: make(map[string]*keyNode)}
}

func insertKeyData(root *keyNode, relative string, data *KeyData) {
	node := root
	if relative != "" {
		for _, name := range strings.Split(relative, "/") {
			folded := strings.ToLower(name)
			child := node.children[folded]
			if child == nil {
				child = newKeyNode(name)
				node.children[folded] = child
			}
			node = child
		}
	}
	node.data = data
}

func reconcileKey(ctx context.Context, root registry.Key, path string, desired *keyNode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, _, err := registry.CreateKey(root, path, registry.READ|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("create registry key %q: %w", path, err)
	}
	var result error
	valueNames, readErr := key.ReadValueNames(-1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	result = errors.Join(result, readErr)
	expectedValues := make(map[string]ValueData)
	if desired.data != nil {
		for _, value := range desired.data.Values {
			expectedValues[strings.ToLower(value.Name)] = value
		}
	}
	for _, name := range valueNames {
		if _, exists := expectedValues[strings.ToLower(name)]; !exists {
			result = errors.Join(result, key.DeleteValue(name))
		}
	}
	for _, value := range expectedValues {
		result = errors.Join(result, writeValue(key, value))
	}
	children, childErr := key.ReadSubKeyNames(-1)
	if errors.Is(childErr, io.EOF) {
		childErr = nil
	}
	result = errors.Join(result, childErr)
	if closeErr := key.Close(); closeErr != nil {
		return errors.Join(result, closeErr)
	}
	if result != nil {
		return result
	}
	seenChildren := make(map[string]struct{}, len(children))
	for _, child := range children {
		folded := strings.ToLower(child)
		childNode := desired.children[folded]
		childPath := path + `\` + child
		if childNode == nil {
			if err := deleteTree(ctx, root, childPath); err != nil {
				return fmt.Errorf("delete extra registry key %q: %w", childPath, err)
			}
			continue
		}
		seenChildren[folded] = struct{}{}
		if err := reconcileKey(ctx, root, childPath, childNode); err != nil {
			return err
		}
	}
	for folded, childNode := range desired.children {
		if _, exists := seenChildren[folded]; exists {
			continue
		}
		childPath := path + `\` + childNode.name
		if err := reconcileKey(ctx, root, childPath, childNode); err != nil {
			return err
		}
	}
	return nil
}

func deleteTree(ctx context.Context, root registry.Key, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := registry.OpenKey(root, path, registry.READ)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	children, readErr := key.ReadSubKeyNames(-1)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if err := errors.Join(readErr, key.Close()); err != nil {
		return err
	}
	for _, child := range children {
		if err := deleteTree(ctx, root, path+`\`+child); err != nil {
			return err
		}
	}
	return registry.DeleteKey(root, path)
}

func wrapError(prefix string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", prefix, err)
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
