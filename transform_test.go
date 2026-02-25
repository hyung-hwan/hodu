package hodu

import (
	"bytes"
	"errors"
	"testing"

	"golang.org/x/text/transform"
)

func TestStringTransformerReplacesAllMatches(t *testing.T) {
	var tf *Transformer
	var out string
	var err error

	tf = NewStringTransformer("cat", "dog")
	out, _, err = transform.String(tf, "cat--cat--catalog")
	if err != nil {
		t.Fatalf("transform failed: %v", err)
	}

	if out != "dog--dog--dogalog" {
		t.Fatalf("unexpected transformed output %q", out)
	}
}

func TestTransformerHandlesNeedleAcrossChunks(t *testing.T) {
	var tf *Transformer
	var dst []byte
	var n_dst int
	var n_src int
	var err error
	var got string

	tf = NewStringTransformer("abc", "X")
	dst = make([]byte, 16)
	n_dst, n_src, err = tf.Transform(dst, []byte("zzab"), false)
	if !errors.Is(err, transform.ErrShortSrc) {
		t.Fatalf("expected ErrShortSrc, got %v", err)
	}
	if n_dst != 2 || n_src != 2 {
		t.Fatalf("unexpected consumption ndst=%d nsrc=%d", n_dst, n_src)
	}
	got = string(dst[:n_dst])
	if got != "zz" {
		t.Fatalf("unexpected first output %q", got)
	}

	dst = make([]byte, 16)
	n_dst, n_src, err = tf.Transform(dst, []byte("abcYY"), true)
	if err != nil {
		t.Fatalf("second transform failed: %v", err)
	}
	if n_src != len("abcYY") {
		t.Fatalf("unexpected second source consumption %d", n_src)
	}
	got = string(dst[:n_dst])
	if got != "XYY" {
		t.Fatalf("unexpected second output %q", got)
	}
}

func TestTransformerReturnsErrShortDst(t *testing.T) {
	var tf *Transformer
	var dst []byte
	var n_dst int
	var n_src int
	var err error

	tf = NewStringTransformer("", "")
	dst = make([]byte, 3)
	n_dst, n_src, err = tf.Transform(dst, []byte("abcdef"), true)

	if !errors.Is(err, transform.ErrShortDst) {
		t.Fatalf("expected ErrShortDst, got %v", err)
	}
	if n_dst != 3 || n_src != 3 {
		t.Fatalf("unexpected consumption ndst=%d nsrc=%d", n_dst, n_src)
	}
	if !bytes.Equal(dst, []byte("abc")) {
		t.Fatalf("unexpected partial output %q", string(dst))
	}
}
