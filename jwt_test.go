package hodu_test

import "crypto/rand"
import "crypto/rsa"
import "hodu"
import "testing"

func TestJwt(t *testing.T) {
	var tok string
	var err error
	          
	type JWTClaim struct {
	     Abc string `json:"abc"`
	     Donkey string `json:"donkey"`
	     IssuedAt int `json:"iat"`
	}         
	          
	var jc JWTClaim
	jc.Abc = "def"
	jc.Donkey = "kong" 
	jc.IssuedAt = 111

	var key *rsa.PrivateKey
	key, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil { t.Fatalf("keygen failure - %s", err.Error()) }

	var j *hodu.JWT[JWTClaim]
	j = hodu.NewJWT(key, &jc)
	tok, err = j.SignRS512()
	if err != nil { t.Fatalf("signing failure - %s", err.Error()) }

	jc = JWTClaim{}
	err = j.VerifyRS512(tok)
	if err != nil { t.Fatalf("verification failure - %s", err.Error()) }

	if jc.Abc != "def" { t.Fatal("decoding failure of Abc field") }
	if jc.Donkey != "kong" { t.Fatal("decoding failure of Donkey field") }
	if jc.IssuedAt != 111 { t.Fatal("decoding failure of Issued field") }
}
