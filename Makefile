NAME=hodu
VERSION=1.0.0

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
	server-ws.go \
	system-freebsd.go \
	system-linux.go

CMD_DATA=\
	cmd/tls.crt \
	cmd/tls.key

CMD_SRCS=\
	cmd/config.go \
	cmd/main.go \

all: $(NAME)

$(NAME): $(SRCS) $(CMD_DATA) $(CMD_SRCS)
	CGO_ENABLED=0 go build -x -ldflags "-X 'main.HODU_NAME=$(NAME)' -X 'main.HODU_VERSION=$(VERSION)'" -o $@ $(CMD_SRCS)

clean:
	go clean -x -i
	rm -f $(NAME)

hodu.pb.go: hodu.proto
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		hodu.proto

hodu_grpc.pb.go: hodu.proto
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		hodu.proto

cmd/tls.crt:
	openssl req -x509 -newkey rsa:4096 -keyout cmd/tls.key -out cmd/tls.crt -sha256 -days 36500 -nodes -subj "/CN=$(NAME)" --addext "subjectAltName=DNS:$(NAME),IP:10.0.0.1,IP:::1"

cmd/tls.key:
	openssl req -x509 -newkey rsa:4096 -keyout cmd/tls.key -out cmd/tls.crt -sha256 -days 36500 -nodes -subj "/CN=$(NAME)" --addext "subjectAltName=DNS:$(NAME),IP:10.0.0.1,IP:::1"

.PHONY: clean certfiles
