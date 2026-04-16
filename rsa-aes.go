package hodu

import "crypto/aes"
import "crypto/cipher"
import "crypto/rand"
import "crypto/rsa"
import "crypto/sha256"
import "encoding/base64"
import "fmt"
import "strconv"
import "strings"
import "time"

// currently, it supports rsa-aes-128-gcm only.
type RSAAES struct {
	key *rsa.PrivateKey
}

type RSAAESToken struct {
	Token string
	IssuedAt time.Time
	ExpiresAt time.Time
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

func (e *RSAAES) EncipherToken(token string, issued_at time.Time, expires_at time.Time) (string, error) {
	var plain string

	plain = base64.RawURLEncoding.EncodeToString([]byte(token)) +
		"|" + strconv.FormatInt(issued_at.Unix(), 10) +
		"|" + strconv.FormatInt(expires_at.Unix(), 10)

	return e.Encipher([]byte(plain))
}

func (e *RSAAES) DecipherToken(doc string, now time.Time) (*RSAAESToken, error) {
	var data []byte
	var parts []string
	var token_data []byte
	var issued_at_n int64
	var expires_at_n int64
	var token RSAAESToken
	var err error

	data, err = e.Decipher(doc)
	if err != nil { return nil, err }

	parts = strings.SplitN(string(data), "|", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid protected token payload")
	}

	token_data, err = base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid protected token text - %s", err.Error())
	}

	issued_at_n, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid protected token issued-at - %s", err.Error())
	}

	expires_at_n, err = strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid protected token expiry - %s", err.Error())
	}

	token.Token = string(token_data)
	token.IssuedAt = time.Unix(issued_at_n, 0)
	token.ExpiresAt = time.Unix(expires_at_n, 0)

	const time_format string = "2006-01-02 15:04:05 -0700"
	if now.Before(token.IssuedAt) {
		return nil, fmt.Errorf("protected token not valid until %s", token.IssuedAt.Format(time_format))
	}
	if !now.Before(token.ExpiresAt) {
		return nil, fmt.Errorf("protected token expired at %s", token.ExpiresAt.Format(time_format))
	}

	return &token, nil
}
