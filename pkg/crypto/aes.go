package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// AESGCM 用于加密敏感字段(AT / cookies / 代理密码)。
// 密钥是 hex 字符串,必须 64 字符(= 32 字节 AES-256 密钥)。
type AESGCM struct {
	aead cipher.AEAD
}

func NewAESGCM(hexKey string) (*AESGCM, error) {
	if len(hexKey) != 64 {
		return nil, fmt.Errorf("aes key must be 64 hex chars (32 bytes), got %d", len(hexKey))
	}
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("decode hex key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return &AESGCM{aead: aead}, nil
}

// Encrypt 返回 base64(nonce || ciphertext_with_tag)。
func (a *AESGCM) Encrypt(plaintext []byte) (string, error) {
	nonce := make([]byte, a.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := a.aead.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt 接受 Encrypt 返回的 base64 字符串。
func (a *AESGCM) Decrypt(b64 string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	ns := a.aead.NonceSize()
	if len(raw) < ns+a.aead.Overhead() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	pt, err := a.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("aead open: %w", err)
	}
	return pt, nil
}

// EncryptString / DecryptString 方便字符串字段使用。
func (a *AESGCM) EncryptString(s string) (string, error) { return a.Encrypt([]byte(s)) }
func (a *AESGCM) DecryptString(b64 string) (string, error) {
	b, err := a.Decrypt(b64)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
