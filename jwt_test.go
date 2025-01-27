package hodu_test

import "hodu"
import "testing"

func TestJwt(t *testing.T) {
	var j hodu.JWT
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
	tok, err = j.Sign(&jc) 
	if err != nil { t.Fatalf("signing failure - %s", err.Error()) }

	err = j.Verify(tok)
	if err != nil { t.Fatalf("verification failure - %s", err.Error()) }
}
