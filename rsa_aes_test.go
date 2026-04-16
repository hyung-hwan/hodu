package hodu

import "bytes"
import "crypto/rand"
import "crypto/rsa"
import "testing"

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
