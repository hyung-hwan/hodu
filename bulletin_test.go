package hodu_test

import "fmt"
import "hodu"
import "sync"
import "testing"
import "time"

func TestBulletin1(t *testing.T) {
	var b *hodu.Bulletin[string]
	var s1 *hodu.BulletinSubscription[string]
	var s2 *hodu.BulletinSubscription[string]
	var wg sync.WaitGroup
	var nmsgs1 int
	var nmsgs2 int

	b = hodu.NewBulletin[string](nil, 100)

	s1, _ = b.Subscribe("t1")
	s2, _ = b.Subscribe("t2")

	wg.Add(1)
	go func() {
		var m string
		var ok bool
		var c1 chan string
		var c2 chan string

		c1 = s1.C
		c2 = s2.C

		defer wg.Done()
		for c1 != nil || c2 != nil {
			select {
				case m, ok = <-c1:
					if ok { fmt.Printf ("s1: %+v\n", m); nmsgs1++ } else { c1 = nil; fmt.Printf ("s1 closed\n")}

				case m, ok = <-c2:
					if ok { fmt.Printf ("s2: %+v\n", m); nmsgs2++ } else { c2 = nil; fmt.Printf ("s2 closed\n") }
			}
		}
	}()

	b.Publish("t1", "donkey")
	b.Publish("t2", "monkey")
	b.Publish("t1", "donkey kong")
	b.Publish("t2", "monkey hong")
	b.Publish("t3", "home")
	b.Publish("t2", "fire")
	b.Publish("t1", "sunflower")
	b.Publish("t2", "itsy bitsy spider")
	b.Publish("t3", "marigold")
	b.Publish("t3", "parrot")
	time.Sleep(100 * time.Millisecond)
	b.Publish("t2", "tiger")
	time.Sleep(100 * time.Millisecond)
	b.Unsubscribe(s2)
	b.Publish("t2", "lion king")
	b.Publish("t2", "fly to the skyp")
	time.Sleep(100 * time.Millisecond)

	b.Block()
	b.UnsubscribeAll()
	wg.Wait()
	fmt.Printf ("---------------------\n")

	if nmsgs1 != 3 { t.Errorf("number of messages for s1 received must be 3, but got %d\n", nmsgs1) }
	if nmsgs2 != 5 { t.Errorf("number of messages for s2 received must be 5, but got %d\n", nmsgs2) }
}

func TestBulletin2(t *testing.T) {
	var b *hodu.Bulletin[string]
	var s1 *hodu.BulletinSubscription[string]
	var s2 *hodu.BulletinSubscription[string]
	var wg sync.WaitGroup
	var nmsgs1 int
	var nmsgs2 int

	b = hodu.NewBulletin[string](nil, 13) // if the size is too small, some messages are lost

	wg.Add(1)
	go b.RunTask(&wg)

	s1, _ = b.Subscribe("")
	s2, _ = b.Subscribe("")

	wg.Add(1)
	go func() {
		var m string
		var ok bool
		var c1 chan string
		var c2 chan string

		c1 = s1.C
		c2 = s2.C

		defer wg.Done()
		for c1 != nil || c2 != nil {
			select {
				case m, ok = <-c1:
					if ok { fmt.Printf ("s1: %+v\n", m); nmsgs1++ } else { c1 = nil; fmt.Printf ("s1 closed\n")}

				case m, ok = <-c2:
					if ok { fmt.Printf ("s2: %+v\n", m); nmsgs2++ } else { c2 = nil; fmt.Printf ("s2 closed\n") }
			}
		}
	}()

	b.Enqueue("donkey")
	b.Enqueue("monkey")
	b.Enqueue("donkey kong")
	b.Enqueue("monkey hong")
	b.Enqueue("home")
	b.Enqueue("fire")
	b.Enqueue("sunflower")
	b.Enqueue("itsy bitsy spider")
	b.Enqueue("marigold")
	b.Enqueue("parrot")
	b.Enqueue("tiger")
	b.Enqueue("walrus")
	b.Enqueue("donkey runs")
	// without this unsubscription may happen before s2.C can receive messages
	// 100 millisconds must be longer than enough for all messages to be received
	time.Sleep(100 * time.Millisecond)
	b.Unsubscribe(s2)
	b.Enqueue("lion king")
	b.Enqueue("fly to the ground")
	b.Enqueue("dig it")
	b.Enqueue("dig it dawg")
	time.Sleep(100 * time.Millisecond)

	b.UnsubscribeAll()
	b.ReqStop()
	wg.Wait()
	fmt.Printf ("---------------------\n")

	if nmsgs1 != 17 { t.Errorf("number of messages for s1 received must be 17, but got %d\n", nmsgs1) }
	if nmsgs2 != 13 { t.Errorf("number of messages for s2 received must be 13, but got %d\n", nmsgs2) }
}
