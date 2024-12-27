package hodu

import "bytes"
import "golang.org/x/text/transform"

type Transformer struct {
	replacement []byte
	needle []byte
	needle_len int
}

func NewBytesTransformer(needle []byte, replacement []byte) *Transformer {
	return &Transformer{needle: needle, replacement: replacement, needle_len: len(needle)}
}

func NewStringTransformer(needle string, replacement string) *Transformer {
	return NewBytesTransformer([]byte(needle), []byte(replacement))
}

func (t *Transformer) Reset() {
	// do nothing
}

func (t *Transformer) Transform(dst []byte, src []byte, at_eof bool) (int, int, error) {
	var n int
	var i int
	var ndst int
	var nsrc int
	var rem int
	var err error

	if t.needle_len <= 0 {
		n, err = t.copy_all(dst, src)
		return n, n, err
	}

	nsrc = 0; ndst = 0
	for {
		i = bytes.Index(src[nsrc:], t.needle)
		if i == -1 { break }

		// copy the part before the match
		n, err = t.copy_all(dst[ndst:], src[nsrc:nsrc+i])
		nsrc += n; ndst += n
		if err != nil { goto done }

		// copy the new value in place of the match
		n, err = t.copy_all(dst[ndst:], t.replacement)
		if err != nil { goto done }
		ndst += n; nsrc += t.needle_len
	}

	if at_eof {
		n, err = t.copy_all(dst[ndst:], src[nsrc:])
		ndst += n; nsrc += n
		goto done
	}

	rem = len(src[nsrc:])
	if rem >= t.needle_len {
		n, err = t.copy_all(dst[ndst:], src[nsrc: nsrc + (rem - t.needle_len) + 1])
		nsrc += n; ndst += n
		if err != nil { goto done }
	}

	// ErrShortSrc means that the source buffer has insufficient data to
	// complete the transformation.
	err = transform.ErrShortSrc

done:
	return ndst, nsrc, err 
}

func (t *Transformer) copy_all(dst []byte, src []byte) (int, error) {
	var n int
	var err error
	n = copy(dst, src)
	// ErrShortDst means that the destination buffer was too short to
	// receive all of the transformed bytes.
	if n < len(src) { err = transform.ErrShortDst }
	return n, err
}
