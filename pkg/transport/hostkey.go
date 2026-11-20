package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"

	"github.com/caddyserver/certmagic"
	"golang.org/x/crypto/ssh"
)

func LoadOrCreateHostKey(ctx context.Context, storage certmagic.Storage, keyPath string) (ssh.Signer, error) {
	if storage.Exists(ctx, keyPath) {
		raw, err := storage.Load(ctx, keyPath)
		if err != nil {
			return nil, fmt.Errorf("load ssh host key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			return nil, fmt.Errorf("parse ssh host key: %w", err)
		}
		return signer, nil
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 host key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "gitproxy-host-key")
	if err != nil {
		return nil, fmt.Errorf("marshal ssh host key: %w", err)
	}
	if err := storage.Store(ctx, keyPath, pem.EncodeToMemory(block)); err != nil {
		return nil, fmt.Errorf("store ssh host key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("host key signer: %w", err)
	}
	return signer, nil
}
