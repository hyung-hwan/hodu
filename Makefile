SRCS=\
	client.go \
	client-ctl.go \
	client-peer.go \
	frame.go \
	hodu.go \
	hodu.pb.go \
	hodu_grpc.pb.go \
	packet.go \
	server.go \
	server-peer.go \
	cmd/main.go

all: hodu

hodu: $(SRCS)
	CGO_ENABLED=0 go build -x -o $@ cmd/main.go

clean:
	go clean -x -i
	rm -f hodu

hodu.pb.go: hodu.proto
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		hodu.proto

hodu_grpc.pb.go: hodu.proto
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		hodu.proto

.PHONY: clean
