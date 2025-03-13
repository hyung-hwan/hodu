package hodu

import "sync/atomic"

type Atom[T any] struct {
	val atomic.Value
}

func (av* Atom[T]) Set(v T) {
	av.val.Store(v)
}

func (av* Atom[T]) Get() T {
	var v interface{}
	v = av.val.Load()
	if v == nil { 
		var t T
		return t // return the zero-value
	}
	return v.(T)
}
