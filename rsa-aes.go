package hodu

import "crypto/aes"
import "crypto/cipher"
import "crypto/rand"
import "crypto/rsa"
import "crypto/sha256"
import "encoding/base64"
import "fmt"
import "strings"

// currently, it supports rsa-aes-128-gcm only.
type RSAAES struct {
	key *rsa.PrivateKey
}

func NewRSAAES(key *rsa.PrivateKey) *RSAAES {
	return &RSAAES{key: key}
}

func (e *RSAAES) Encipher(data []byte) (string, error) {
	var aes_key []byte
	var block cipher.Block
	var gcm cipher.AEAD
	var nonce []byte
	var ciphertext []byte
	var encrypted_key []byte
	var err error

	if e.key == nil {
		return "", fmt.Errorf("missing rsa key")
	}

	aes_key = make([]byte, 32)
	_, err = rand.Read(aes_key)
	if err != nil { return "", err }

	block, err = aes.NewCipher(aes_key)
	if err != nil { return "", err }

	gcm, err = cipher.NewGCM(block)
	if err != nil { return "", err }

	nonce = make([]byte, gcm.NonceSize())
	_, err = rand.Read(nonce)
	if err != nil { return "", err }

	ciphertext = gcm.Seal(nil, nonce, data, nil)

	encrypted_key, err = rsa.EncryptOAEP(sha256.New(), rand.Reader, &e.key.PublicKey, aes_key, nil)
	if err != nil { return "", err }

	return base64.StdEncoding.EncodeToString(encrypted_key) +
		"." + base64.StdEncoding.EncodeToString(nonce) +
		"." + base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (e *RSAAES) Decipher(doc string) ([]byte, error) {
	var parts []string
	var encrypted_key []byte
	var nonce []byte
	var ciphertext []byte
	var aes_key []byte
	var block cipher.Block
	var gcm cipher.AEAD
	var plaintext []byte
	var err error

	if e.key == nil {
		return nil, fmt.Errorf("missing rsa key")
	}

	parts = strings.Split(doc, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid serialized token document")
	}

	encrypted_key, err = base64.StdEncoding.DecodeString(parts[0])
	if err != nil { return nil, fmt.Errorf("invalid encrypted key - %s", err.Error()) }

	nonce, err = base64.StdEncoding.DecodeString(parts[1])
	if err != nil { return nil, fmt.Errorf("invalid nonce - %s", err.Error()) }

	ciphertext, err = base64.StdEncoding.DecodeString(parts[2])
	if err != nil { return nil, fmt.Errorf("invalid ciphertext - %s", err.Error()) }

	aes_key, err = rsa.DecryptOAEP(sha256.New(), rand.Reader, e.key, encrypted_key, nil)
	if err != nil { return nil, fmt.Errorf("failed to decrypt aes key - %s", err.Error()) }

	block, err = aes.NewCipher(aes_key)
	if err != nil { return nil, fmt.Errorf("invalid aes key - %s", err.Error()) }

	gcm, err = cipher.NewGCM(block)
	if err != nil { return nil, err }
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid nonce size %d", len(nonce))
	}

	plaintext, err = gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil { return nil, fmt.Errorf("failed to decrypt ciphertext - %s", err.Error()) }

	return plaintext, nil
}
