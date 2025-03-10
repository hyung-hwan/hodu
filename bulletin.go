package hodu

import "container/list"
import "errors"
import "sync"

type BulletinSubscription[T interface{}] struct {
	C chan T
	b *Bulletin[T]
	topic string
	node *list.Element
}

type BulletinSubscriptionList = *list.List

type BulletinSubscriptionMap map[string]BulletinSubscriptionList

type Bulletin[T interface{}] struct {
	sbsc_map BulletinSubscriptionMap
	sbsc_mtx sync.RWMutex
	closed bool
}

func NewBulletin[T interface{}]() *Bulletin[T] {
	return &Bulletin[T]{
		sbsc_map: make(BulletinSubscriptionMap, 0),
	}
}

func (b *Bulletin[T]) unsubscribe_all_nolock() {
	var topic string
	var sl BulletinSubscriptionList

	for topic, sl = range b.sbsc_map {
		var sbsc *BulletinSubscription[T]
		var e *list.Element

		for e = sl.Front(); e != nil; e = e.Next() {
			sbsc = e.Value.(*BulletinSubscription[T])
			close(sbsc.C)
			sbsc.b = nil
			sbsc.node = nil
		}

		delete(b.sbsc_map, topic)
	}

	b.closed = true
}

func (b *Bulletin[T]) UnsubscribeAll() {
	b.sbsc_mtx.Lock()
	b.unsubscribe_all_nolock()
	b.sbsc_mtx.Unlock()
}

func (b *Bulletin[T]) Close() {
	b.sbsc_mtx.Lock()
	if !b.closed {
		b.unsubscribe_all_nolock()
		b.closed = true
	}
	b.sbsc_mtx.Unlock()
}

func (b *Bulletin[T]) Subscribe(topic string) (*BulletinSubscription[T], error) {
	var sbsc BulletinSubscription[T]
	var sbsc_list BulletinSubscriptionList
	var ok bool

	if b.closed { return nil, errors.New("closed bulletin") }

	sbsc.C = make(chan T, 128) // TODO: size?
	sbsc.b = b
	sbsc.topic = topic

	b.sbsc_mtx.Lock()
	sbsc_list, ok = b.sbsc_map[topic]
	if !ok {
		sbsc_list = list.New()
		b.sbsc_map[topic] = sbsc_list
	}
	sbsc.node = sbsc_list.PushBack(&sbsc)
	b.sbsc_mtx.Unlock()
	return &sbsc, nil
}

func (b *Bulletin[T]) Unsubscribe(sbsc *BulletinSubscription[T]) {
	if sbsc.b == b && sbsc.node != nil {
		var sl BulletinSubscriptionList
		var ok bool

		b.sbsc_mtx.Lock()
		sl, ok = b.sbsc_map[sbsc.topic]
		if ok {
			sl.Remove(sbsc.node)
			close(sbsc.C)
			sbsc.node = nil
			sbsc.b = nil
		}
		b.sbsc_mtx.Unlock()
	}
}

func (b *Bulletin[T]) Publish(topic string, data T) {
	var sl BulletinSubscriptionList
	var ok bool
	var topics [2]string
	var t string

	if b.closed { return }
	if topic == "" { return }

	topics[0] = topic
	topics[1] = ""

	b.sbsc_mtx.Lock()
	for _, t = range topics {
		sl, ok = b.sbsc_map[t]
		if ok {
			var sbsc *BulletinSubscription[T]
			var e *list.Element
			for e = sl.Front(); e != nil; e = e.Next() {
				sbsc = e.Value.(*BulletinSubscription[T])
				select {
					case sbsc.C <- data:
						// ok. could be written.
					default:
						// channel full. discard it
				}
			}
		}
	}
	b.sbsc_mtx.Unlock()
}

