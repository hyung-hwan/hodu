package hodu_test

import "hodu"
import "slices"
import "sync"
import "testing"
import "time"

func read_n_messages(t *testing.T, ch <-chan string, n int, timeout time.Duration) []string {
	var got []string
	var timer *time.Timer
	var msg string
	var ok bool

	t.Helper()

	got = make([]string, 0, n)
	timer = time.NewTimer(timeout)
	defer timer.Stop()

	for len(got) < n {
		select {
		case msg, ok = <-ch:
			if !ok {
				t.Fatalf("channel closed after %d/%d messages", len(got), n)
			}
			got = append(got, msg)
		case <-timer.C:
			t.Fatalf("timed out waiting for message %d/%d", len(got)+1, n)
		}
	}

	return got
}

func require_closed(t *testing.T, ch <-chan string, timeout time.Duration) {
	var ok bool

	t.Helper()

	select {
	case _, ok = <-ch:
		if ok {
			t.Fatal("expected closed channel")
		}
	case <-time.After(timeout):
		t.Fatal("expected channel to be closed")
	}
}

func TestBulletinPublishByTopic(t *testing.T) {
	var b *hodu.Bulletin[string]
	var s1 *hodu.BulletinSubscription[string]
	var s2 *hodu.BulletinSubscription[string]
	var err error
	var want_s1 []string
	var want_s2 []string
	var got_s1 []string
	var got_s2 []string
	var msg string
	var ok bool

	b = hodu.NewBulletin[string](nil, 100)

	s1, err = b.Subscribe("t1")
	if err != nil {
		t.Fatalf("failed to subscribe t1: %v", err)
	}
	s2, err = b.Subscribe("t2")
	if err != nil {
		t.Fatalf("failed to subscribe t2: %v", err)
	}

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
	b.Publish("t2", "tiger")

	want_s1 = []string{"donkey", "donkey kong", "sunflower"}
	want_s2 = []string{"monkey", "monkey hong", "fire", "itsy bitsy spider", "tiger"}
	got_s1 = read_n_messages(t, s1.C, len(want_s1), time.Second)
	got_s2 = read_n_messages(t, s2.C, len(want_s2), time.Second)

	if !slices.Equal(got_s1, want_s1) {
		t.Fatalf("unexpected t1 messages: got=%v want=%v", got_s1, want_s1)
	}
	if !slices.Equal(got_s2, want_s2) {
		t.Fatalf("unexpected t2 messages: got=%v want=%v", got_s2, want_s2)
	}

	b.Unsubscribe(s2)
	require_closed(t, s2.C, 200*time.Millisecond)

	b.Publish("t2", "lion king")
	b.Publish("t2", "fly to the sky")
	select {
	case msg, ok = <-s1.C:
		if ok {
			t.Fatalf("unexpected message on t1 subscription: %q", msg)
		}
		t.Fatal("t1 subscription closed unexpectedly")
	default:
	}

	b.UnsubscribeAll()
	require_closed(t, s1.C, 200*time.Millisecond)

	_, err = b.Subscribe("t1")
	if err == nil {
		t.Fatal("expected Subscribe to fail after UnsubscribeAll")
	}
}

func TestBulletinRunTaskBroadcastAndUnsubscribe(t *testing.T) {
	var b *hodu.Bulletin[string]
	var wg sync.WaitGroup
	var s1 *hodu.BulletinSubscription[string]
	var s2 *hodu.BulletinSubscription[string]
	var err error
	var first_batch []string
	var second_batch []string
	var got_s2_first []string
	var want_s1 []string
	var got_s1 []string
	var i int

	b = hodu.NewBulletin[string](nil, 13)

	wg.Add(1)
	go b.RunTask(&wg)
	defer func() {
		b.ReqStop()
		wg.Wait()
	}()

	s1, err = b.Subscribe("")
	if err != nil {
		t.Fatalf("failed to subscribe s1: %v", err)
	}
	s2, err = b.Subscribe("")
	if err != nil {
		t.Fatalf("failed to subscribe s2: %v", err)
	}

	first_batch = []string{
		"donkey",
		"monkey",
		"donkey kong",
		"monkey hong",
		"home",
		"fire",
		"sunflower",
		"itsy bitsy spider",
		"marigold",
		"parrot",
		"tiger",
		"walrus",
		"donkey runs",
	}
	for i = 0; i < len(first_batch); i++ {
		b.Enqueue(first_batch[i])
	}

	got_s2_first = read_n_messages(t, s2.C, len(first_batch), 2*time.Second)
	if !slices.Equal(got_s2_first, first_batch) {
		t.Fatalf("unexpected first batch on s2: got=%v want=%v", got_s2_first, first_batch)
	}

	b.Unsubscribe(s2)
	require_closed(t, s2.C, 200*time.Millisecond)

	second_batch = []string{"lion king", "fly to the ground", "dig it", "dig it dawg"}
	for i = 0; i < len(second_batch); i++ {
		b.Enqueue(second_batch[i])
	}

	want_s1 = make([]string, 0, len(first_batch)+len(second_batch))
	want_s1 = append(want_s1, first_batch...)
	want_s1 = append(want_s1, second_batch...)
	got_s1 = read_n_messages(t, s1.C, len(want_s1), 2*time.Second)
	if !slices.Equal(got_s1, want_s1) {
		t.Fatalf("unexpected broadcast messages on s1: got=%v want=%v", got_s1, want_s1)
	}

	b.UnsubscribeAll()
	require_closed(t, s1.C, 200*time.Millisecond)
}

func TestBulletinEnqueueOverflowDropsOldest(t *testing.T) {
	var b *hodu.Bulletin[int]
	var values []int
	var want []int
	var i int
	var got int
	var ok bool

	b = hodu.NewBulletin[int](nil, 3)
	values = []int{1, 2, 3, 4, 5}
	for i = 0; i < len(values); i++ {
		b.Enqueue(values[i])
	}

	want = []int{3, 4, 5}
	for i = 0; i < len(want); i++ {
		got, ok = b.Dequeue()
		if !ok {
			t.Fatalf("missing item at index %d", i)
		}
		if got != want[i] {
			t.Fatalf("unexpected dequeued value at index %d: got=%d want=%d", i, got, want[i])
		}
	}

	_, ok = b.Dequeue()
	if ok {
		t.Fatal("expected queue to be empty")
	}
}
