package hodu

import "crypto"
import "crypto/hmac"
import "crypto/rand"
import "crypto/rsa"
import "encoding/base64"
import "encoding/json"
import "fmt"
import "hash"
import "strings"

func Sign(data []byte, privkey *rsa.PrivateKey) ([]byte, error) {
	var h hash.Hash

	h = crypto.SHA512.New()
	h.Write(data)

	fmt.Printf("%+v\n", h.Sum(nil))
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

type JWT struct {
}

type JWTHeader struct {
	Algo string `json:"alg"`
	Type string `json:"typ"`
}

type JWTClaimMap map[string]interface{}

func (j *JWT) Sign(claims interface{}) (string, error) {
	var h JWTHeader
	var hb []byte
	var cb []byte
	var ss string
	var sb []byte
	var err error

	h.Algo = "HS512"
	h.Type = "JWT"

	hb, err = json.Marshal(h)	
	if err != nil { return "", err }

	cb, err = json.Marshal(claims)
	if err != nil { return "", err }

	ss = base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	sb, err = SignHS512([]byte(ss), "hello")
	if err != nil { return "", err }

fmt.Printf ("%+v %+v %s\n", string(hb), string(cb), (ss + "." + base64.RawURLEncoding.EncodeToString(sb)))
	return ss + "." + base64.RawURLEncoding.EncodeToString(sb), nil
}

func (j *JWT) Verify(tok string) error {
	var segs []string
	var hb []byte
	var cb []byte
	var sb []byte
	var jh JWTHeader
	var jcm JWTClaimMap
	var x string
	var err error

	segs = strings.Split(tok, ".")
	if len(segs) != 3 { return fmt.Errorf("invalid token") }

	hb, err = base64.RawURLEncoding.DecodeString(segs[0])
	if err != nil { return fmt.Errorf("invalid header - %s", err.Error()) }
	err = json.Unmarshal(hb, &jh)
	if err != nil { return fmt.Errorf("invalid header - %s", err.Error()) }
fmt.Printf ("DECODED HEADER [%+v]\n", jh)

	cb, err = base64.RawURLEncoding.DecodeString(segs[1])
	if err != nil { return fmt.Errorf("invalid claims - %s", err.Error()) }
	err = json.Unmarshal(cb, &jcm)
	if err != nil { return fmt.Errorf("invalid header - %s", err.Error()) }
fmt.Printf ("DECODED CLAIMS [%+v]\n", jcm)

	x, err = j.Sign(jcm)
	if err != nil { return err }
fmt.Printf ("VERIFICATION OK...\n")

	if x != tok { return fmt.Errorf("signature mismatch") }

//	sb, err = base64.RawURLEncoding.DecodeString(segs[2])
//	if err != nil { return fmt.Errorf("invalid signature - %s", err.Error()) }

	_ = sb

	return nil
}
