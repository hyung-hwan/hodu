package hodu

import "bytes"
import "crypto/rand"
import "crypto/rsa"
import "testing"
import "time"

func TestRSAAESRoundTrip(t *testing.T) {
	var key *rsa.PrivateKey
	var codec *RSAAES
	var input []byte
	var doc string
	var output []byte
	var err error

	key, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate rsa key - %s", err.Error())
	}

	codec = NewRSAAES(key)
	input = []byte("client-token-123")

	doc, err = codec.Encipher(input)
	if err != nil {
		t.Fatalf("failed to encipher - %s", err.Error())
	}

	output, err = codec.Decipher(doc)
	if err != nil {
		t.Fatalf("failed to decipher - %s", err.Error())
	}

	if !bytes.Equal(output, input) {
		t.Fatalf("unexpected plaintext %q", string(output))
	}
}

func TestRSAAESInvalidDocument(t *testing.T) {
	var key *rsa.PrivateKey
	var codec *RSAAES
	var err error

	key, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate rsa key - %s", err.Error())
	}

	codec = NewRSAAES(key)
	_, err = codec.Decipher("broken")
	if err == nil {
		t.Fatal("expected decipher failure for invalid document")
	}
}

func TestRSAAESTokenRoundTrip(t *testing.T) {
	var key *rsa.PrivateKey
	var codec *RSAAES
	var issued_at time.Time
	var expires_at time.Time
	var doc string
	var tok *RSAAESToken
	var err error

	key, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate rsa key - %s", err.Error())
	}

	codec = NewRSAAES(key)
	issued_at = time.Unix(1700000000, 0)
	expires_at = issued_at.Add(30 * time.Second)

	doc, err = codec.EncipherToken("client-token-xyz", issued_at, expires_at)
	if err != nil {
		t.Fatalf("failed to encipher token - %s", err.Error())
	}

	tok, err = codec.DecipherToken(doc, issued_at.Add(5 * time.Second))
	if err != nil {
		t.Fatalf("failed to decipher token - %s", err.Error())
	}

	if tok.Token != "client-token-xyz" {
		t.Fatalf("unexpected token %q", tok.Token)
	}
	if !tok.IssuedAt.Equal(issued_at) {
		t.Fatalf("unexpected issued-at %v", tok.IssuedAt)
	}
	if !tok.ExpiresAt.Equal(expires_at) {
		t.Fatalf("unexpected expires-at %v", tok.ExpiresAt)
	}
}
