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
	server-ctl.go \
	server-peer.go \
	server-ws.go

CMD_DATA=\
	cmd/tls.crt \
	cmd/tls.key

CMD_SRCS=\
	cmd/config.go \
	cmd/main.go \

all: hodu

hodu: $(SRCS) $(CMD_DATA) $(CMD_SRCS)
	CGO_ENABLED=0 go build -x -o $@ $(CMD_SRCS)

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

cmd/tls.crt:
	openssl req -x509 -newkey rsa:4096 -keyout cmd/tls.key -out cmd/tls.crt -sha256 -days 36500 -nodes -subj "/CN=hodu"

cmd/tls.key:
	openssl req -x509 -newkey rsa:4096 -keyout cmd/tls.key -out cmd/tls.crt -sha256 -days 36500 -nodes -subj "/CN=hodu"

.PHONY: clean
