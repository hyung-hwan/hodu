package hodu

type Message struct {
}

type Subscriber struct {
}

type Bulletin struct {
}

func NewBulletin() *Bulletin {
	return &Bulletin{}
}

func (b *Bulletin) Subscribe(topic string) {

}

func (b *Bulletin) Publish(topic string,  data interface{}) {
}
