package hodu

import "container/list"
import "container/ring"
import "errors"
import "sync"
import "time"

type BulletinSubscription[T interface{}] struct {
	C chan T
	b *Bulletin[T]
	topic string
	node *list.Element
}

type BulletinSubscriptionList = *list.List

type BulletinSubscriptionMap map[string]BulletinSubscriptionList

type Bulletin[T interface{}] struct {
	svc Service

	sbsc_map BulletinSubscriptionMap
	sbsc_list *list.List
	sbsc_mtx sync.RWMutex
	blocked bool

	r_mtx sync.RWMutex
	r *ring.Ring
	r_head *ring.Ring
	r_tail *ring.Ring
	r_len int
	r_cap int
	r_chan chan struct{}
	stop_chan chan struct{}
}

func NewBulletin[T interface{}](svc Service, capa int) *Bulletin[T] {
	var r *ring.Ring

	r = ring.New(capa)
	return &Bulletin[T]{
		sbsc_map: make(BulletinSubscriptionMap, 0),
		sbsc_list: list.New(),
		r: r,
		r_head: r,
		r_tail: r,
		r_cap: capa,
		r_len: 0,
		r_chan: make(chan struct{}, 1),
		stop_chan: make(chan struct{}, 1),
	}
}

func (b *Bulletin[T]) unsubscribe_list_nolock(sl BulletinSubscriptionList) {
	var sbsc *BulletinSubscription[T]
	var e *list.Element

	for e = sl.Front(); e != nil; e = e.Next() {
		sbsc = e.Value.(*BulletinSubscription[T])
		sl.Remove(sbsc.node)
		close(sbsc.C)
		sbsc.b = nil
		sbsc.node = nil
	}
}

func (b *Bulletin[T]) unsubscribe_all_nolock() {
	var topic string
	var sl BulletinSubscriptionList

	for topic, sl = range b.sbsc_map {
		b.unsubscribe_list_nolock(sl)
		delete(b.sbsc_map, topic)
	}

	b.unsubscribe_list_nolock(b.sbsc_list)
	b.blocked = true
}

func (b *Bulletin[T]) UnsubscribeAll() {
	b.sbsc_mtx.Lock()
	b.unsubscribe_all_nolock()
	b.sbsc_mtx.Unlock()
}

func (b *Bulletin[T]) Block() {
	b.sbsc_mtx.Lock()
	b.blocked = true
	b.sbsc_mtx.Unlock()
}

func (b *Bulletin[T]) Unblock() {
	b.sbsc_mtx.Lock()
	b.blocked = false
	b.sbsc_mtx.Unlock()
}

func (b *Bulletin[T]) Subscribe(topic string) (*BulletinSubscription[T], error) {
	var sbsc BulletinSubscription[T]

	b.sbsc_mtx.Lock()
	if b.blocked {
		b.sbsc_mtx.Unlock()
		return nil, errors.New("blocked")
	}

	sbsc.C = make(chan T, 128) // TODO: size?
	sbsc.b = b
	sbsc.topic = topic

	if topic == "" {
		sbsc.node = b.sbsc_list.PushBack(&sbsc)
	} else {
		var sbsc_list BulletinSubscriptionList
		var ok bool

		sbsc_list, ok = b.sbsc_map[topic]
		if !ok {
			sbsc_list = list.New()
			b.sbsc_map[topic] = sbsc_list
		}
		sbsc.node = sbsc_list.PushBack(&sbsc)
	}
	b.sbsc_mtx.Unlock()
	return &sbsc, nil
}

func (b *Bulletin[T]) Unsubscribe(sbsc *BulletinSubscription[T]) {
	if sbsc.b == b && sbsc.node != nil {
		var sl BulletinSubscriptionList
		var ok bool

		b.sbsc_mtx.Lock()
		if sbsc.topic == "" {
			b.sbsc_list.Remove(sbsc.node)
			close(sbsc.C)
			sbsc.node = nil
			sbsc.b = nil
		} else {
			sl, ok = b.sbsc_map[sbsc.topic]
			if ok {
				sl.Remove(sbsc.node)
				close(sbsc.C)
				sbsc.node = nil
				sbsc.b = nil
			}
		}
		b.sbsc_mtx.Unlock()
	}
}

func (b *Bulletin[T]) Publish(topic string, data T) {
	var sl BulletinSubscriptionList
	var ok bool
	if topic == "" { return }

	b.sbsc_mtx.Lock()
	if b.blocked {
		b.sbsc_mtx.Unlock()
		return
	}

	sl, ok = b.sbsc_map[topic]
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
	b.sbsc_mtx.Unlock()
}

func (b *Bulletin[T]) Enqueue(data T) {
	// hopefuly, it's fater to use a single mutex, a ring buffer, and a notification channel than
	// to use a channel to pass messages. TODO: performance verification
	b.r_mtx.Lock()
	if b.blocked {
		b.r_mtx.Unlock()
		return
	}

	if b.r_len < b.r_cap {
		b.r_len++
	} else {
		b.r_head = b.r_head.Next()
	}
	b.r_tail.Value = data // update the value at the current position
	b.r_tail = b.r_tail.Next() // move the current position
	select {
		case b.r_chan <- struct{}{}:
			// write success
		default:
			// don't care if not writable
	}
	b.r_mtx.Unlock()
}

func (b *Bulletin[T]) Dequeue() (T, bool) {
	var v T
	var ok bool

	b.r_mtx.Lock()

	if b.r_len > 0 {
		v = b.r_head.Value.(T) // store the value for returning
		b.r_head.Value = nil // nullify the value
		b.r_head = b.r_head.Next() // advance the head position
		b.r_len--
		ok = true
	}

	b.r_mtx.Unlock()
	return v, ok
}

func (b *Bulletin[T]) RunTask(wg *sync.WaitGroup) {
	var done bool
	var tmr *time.Timer

	defer wg.Done()

	tmr = time.NewTimer(3 * time.Second)
	for !done {
		var msg T
		var ok bool

		msg, ok = b.Dequeue()
		if !ok {
			select {
				case <-b.stop_chan:
					// this may break the loop prematurely while there
					// are messages to read as it uses two different channels:
					// one for stop, another for notification
					done = true
				case <-b.r_chan:
					// noti received.
					tmr.Stop()
					tmr.Reset(3 * time.Second)
				case <-tmr.C:
					// try to dequeue again
					tmr.Reset(3 * time.Second)
			}
		} else {
			// forward msg to all subscribers...
			var e *list.Element
			var sbsc *BulletinSubscription[T]

			tmr.Stop()

			b.sbsc_mtx.Lock()
			for e = b.sbsc_list.Front(); e != nil; e = e.Next() {
				sbsc = e.Value.(*BulletinSubscription[T])
				select {
					case sbsc.C <- msg:
						// ok. could be written.
					default:
						// channel full. discard it
				}
			}
			b.sbsc_mtx.Unlock()
		}
	}

	tmr.Stop()
}

func (b *Bulletin[T]) ReqStop() {
	select {
		case b.stop_chan <- struct{}{}:
			// write success
		default:
			// ignore failure
	}
}
