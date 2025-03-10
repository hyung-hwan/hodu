package hodu

import "container/list"
import "sync"

type BulletinChan = chan interface{}

type BulletinSubscription struct {
	c chan interface{}
	b *Bulletin
	topic string
	node *list.Element
}

type BulletinSubscriptionList = *list.List
type BulletinSubscriptionMap map[string]BulletinSubscriptionList

type Bulletin struct {
	sbsc_map BulletinSubscriptionMap
	sbsc_mtx sync.RWMutex
}

func NewBulletin() *Bulletin {
	return &Bulletin{
		sbsc_map:  make(BulletinSubscriptionMap, 0),
	}
}

func (b *Bulletin) Subscribe(topic string) *BulletinSubscription {
	var sbsc BulletinSubscription
	var sbsc_list BulletinSubscriptionList
	var ok bool

	sbsc.b = b
	sbsc.c = make(chan interface{})
	sbsc.topic = topic
	b.sbsc_mtx.Lock()

	sbsc_list, ok = b.sbsc_map[topic]
	if !ok {
		sbsc_list = list.New()
		b.sbsc_map[topic] = sbsc_list
	}


	sbsc.node = sbsc_list.PushBack(&sbsc)
	b.sbsc_mtx.Unlock()
	return &sbsc
}

func (b *Bulletin) Unsbsccribe(sbsc *BulletinSubscription) {
	if sbsc.b == b {
		var sl BulletinSubscriptionList
		var ok bool

		b.sbsc_mtx.Lock()
		sl, ok = b.sbsc_map[sbsc.topic]
		if ok { sl.Remove(sbsc.node) }
		b.sbsc_mtx.Unlock()
	}
}

func (b *Bulletin) Publish(topic string,  data interface{}) {
	var sl BulletinSubscriptionList
	var ok bool

	b.sbsc_mtx.Lock()
	sl, ok = b.sbsc_map[topic]
	if ok {
		var sbsc *BulletinSubscription
		var e *list.Element
		for e = sl.Front(); e != nil; e = e.Next() {
			sbsc = e.Value.(*BulletinSubscription)
			sbsc.c <- data
		}
	}
	b.sbsc_mtx.Unlock()
}

func (s *BulletinSubscription) Receive() interface{} {
	var x interface{}
	x = <- s.c
	return x
}
