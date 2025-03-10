package hodu_test

import "fmt"
import "hodu"
import "testing"

func TestBulletin(t *testing.T) {
	var b *hodu.Bulletin
	var s1 *hodu.BulletinSubscription
	var s2 *hodu.BulletinSubscription

	b = hodu.NewBulletin()

	s1 = b.Subscribe("t1")
	s2 = b.Subscribe("t2")

	go func() {
		fmt.Printf ("s1: %+v\n", s1.Receive())
	}()

	go func() {
		fmt.Printf ("s2: %+v\n", s2.Receive())
	}()

	b.Publish("t1", "donkey")
}

