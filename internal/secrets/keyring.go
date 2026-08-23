package secrets

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const serviceName = "SaveKnot"

type Keyring struct{}

func (Keyring) Set(id, secret string) error {
	if err := keyring.Set(serviceName, id, secret); err != nil {
		return fmt.Errorf("store R2 secret in the operating system credential store: %w", err)
	}
	return nil
}

func (Keyring) Get(id string) (string, error) {
	secret, err := keyring.Get(serviceName, id)
	if err != nil {
		return "", fmt.Errorf("load R2 secret from the operating system credential store: %w", err)
	}
	return secret, nil
}

func (Keyring) Delete(id string) error {
	if err := keyring.Delete(serviceName, id); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("delete R2 secret from the operating system credential store: %w", err)
	}
	return nil
}
