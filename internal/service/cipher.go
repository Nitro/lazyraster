package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	ddTracer "gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
)

type cipherStorage interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, payload io.Reader) error
}

// Cipher act as a proxy layer to encrypt and decrypt the storage.
type Cipher struct {
	Key     string
	Storage cipherStorage

	client cipher.AEAD
}

// Init the internal state.
func (c *Cipher) Init() error {
	if c.Key == "" {
		return errors.New("internal/service/Cipher.Key can't be empty")
	}
	if c.Storage == nil {
		return errors.New("internal/service/Cipher.Storage can't be nil")
	}

	b, err := aes.NewCipher([]byte(c.Key))
	if err != nil {
		return fmt.Errorf("fail to create a cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(b)
	if err != nil {
		return fmt.Errorf("fail to create a gcm cipher: %w", err)
	}
	c.client = gcm

	return nil
}

// Get is used to decrypt the content before return.
func (c Cipher) Get(ctx context.Context, key string) (_ io.ReadCloser, err error) {
	span, ctx := startSpan(ctx, "Cipher.Get")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	reader, err := c.Storage.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, nil
	}
	defer reader.Close()

	payload, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("fail to read payload: %w", err)
	}

	nonceSize := c.client.NonceSize()
	if len(payload) < nonceSize {
		return nil, errors.New("payload smaller than nonce size")
	}

	nonce, ciphertext := payload[:nonceSize], payload[nonceSize:]
	result, err := c.client.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("fail to decrypt payload: %w", err)
	}

	return io.NopCloser(bytes.NewBuffer(result)), nil
}

// Put is used to encrypt the content.
func (c Cipher) Put(ctx context.Context, key string, reader io.Reader) (err error) {
	span, ctx := startSpan(ctx, "Cipher.Get")
	defer func() { span.Finish(ddTracer.WithError(err)) }()

	payload, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("fail to read payload: %w", err)
	}

	nonce := make([]byte, c.client.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("fail to initialize the nonce: %w", err)
	}

	result := c.client.Seal(nonce, nonce, payload, nil)
	if err := c.Storage.Put(ctx, key, bytes.NewBuffer(result)); err != nil {
		return fmt.Errorf("fail to put object at the storage: %w", err)
	}

	return nil
}
