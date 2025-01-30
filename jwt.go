package hodu

import "crypto"
//import "crypto/hmac"
import "crypto/rand"
import "crypto/rsa"
import "encoding/base64"
import "encoding/json"
import "fmt"
import "hash"
import "strings"

/*
func Sign(data []byte, privkey *rsa.PrivateKey) ([]byte, error) {
	var h hash.Hash

	h = crypto.SHA512.New()
	h.Write(data)

	//fmt.Printf("%+v\n", h.Sum(nil))
	return rsa.SignPKCS1v15(rand.Reader, privkey, crypto.SHA512, h.Sum(nil))
}

func Verify(data []byte, pubkey *rsa.PublicKey, sig []byte) error {
	var h hash.Hash
	
	h = crypto.SHA512.New()
	h.Write(data)

	return rsa.VerifyPKCS1v15(pubkey, crypto.SHA512, h.Sum(nil), sig)
}

func SignHS512(data []byte, key string) ([]byte, error) {
	var h hash.Hash

	h = hmac.New(crypto.SHA512.New, []byte(key))
	h.Write(data)

	return h.Sum(nil), nil
}

func VerifyHS512(data []byte, key string, sig []byte) error {
	var h hash.Hash
	
	h = crypto.SHA512.New()
	h.Write(data)

	if !hmac.Equal(h.Sum(nil), sig) { return fmt.Errorf("invalid signature") }
	return nil
}
*/

type JWT[T any] struct {
	key *rsa.PrivateKey
	claims *T
}

type JWTHeader struct {
	Algo string `json:"alg"`
	Type string `json:"typ"`
}

type JWTClaimMap map[string]interface{}

func NewJWT[T any](key *rsa.PrivateKey, claims *T) *JWT[T] {
	return &JWT[T]{key: key, claims: claims}
}

func (j *JWT[T]) SignRS512() (string, error) {
	var h JWTHeader
	var hb []byte
	var cb []byte
	var ss string
	var sb []byte
	var hs hash.Hash
	var err error

	h.Algo = "RS512"
	h.Type = "JWT"

	hb, err = json.Marshal(h)	
	if err != nil { return "", err }

	cb, err = json.Marshal(j.claims)
	if err != nil { return "", err }

	ss = base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)

	hs = crypto.SHA512.New()
	hs.Write([]byte(ss))

	sb, err = rsa.SignPKCS1v15(rand.Reader, j.key, crypto.SHA512, hs.Sum(nil))
	if err != nil { return "", err }

//fmt.Printf ("%+v %+v %s\n", string(hb), string(cb), (ss + "." + base64.RawURLEncoding.EncodeToString(sb)))
	return ss + "." + base64.RawURLEncoding.EncodeToString(sb), nil
}

func (j *JWT[T]) VerifyRS512(tok string) error {
	var segs []string
	var hb []byte
	var cb []byte
	var ss []byte
	var jh JWTHeader
	var hs hash.Hash
	var err error

	segs = strings.Split(tok, ".")
	if len(segs) != 3 { return fmt.Errorf("invalid token") }

	hb, err = base64.RawURLEncoding.DecodeString(segs[0])
	if err != nil { return fmt.Errorf("invalid header - %s", err.Error()) }
	err = json.Unmarshal(hb, &jh)
	if err != nil { return fmt.Errorf("invalid header - %s", err.Error()) }

	if jh.Algo != "RS512" || jh.Type != "JWT" { return fmt.Errorf("invalid header content %+v", jh) }

	cb, err = base64.RawURLEncoding.DecodeString(segs[1])
	if err != nil { return fmt.Errorf("invalid claims - %s", err.Error()) }
	err = json.Unmarshal(cb, j.claims)
	if err != nil { return fmt.Errorf("invalid claims - %s", err.Error()) }

	ss, err = base64.RawURLEncoding.DecodeString(segs[2])
	if err != nil { return fmt.Errorf("invalid signature - %s", err.Error()) }

	hs = crypto.SHA512.New()
	hs.Write([]byte(segs[0]))
	hs.Write([]byte("."))
	hs.Write([]byte(segs[1]))
	err = rsa.VerifyPKCS1v15(&j.key.PublicKey, crypto.SHA512, hs.Sum(nil), ss)
	if err != nil { return fmt.Errorf("unverifiable signature - %s", err.Error()) }

	return nil
}
