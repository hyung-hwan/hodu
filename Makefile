# make
# make GOARCH=386
# make GOARCH=amd64
# make GOOS=linux GOARCH=mips
#
# 'go tool dist list' for available os and architextures

NAME=hodu
VERSION=1.0.0

SRCS=\
	atom.go \
	bulletin.go \
	client.go \
	client-ctl.go \
	client-metrics.go \
	client-peer.go \
	client-pty.go \
	hodu.go \
	hodu.pb.go \
	hodu_grpc.pb.go \
	jwt.go \
	packet.go \
	pty.go \
	server.go \
	server-ctl.go \
	server-metrics.go \
	server-peer.go \
	server-pty.go \
	server-pxy.go \
	server-rpx.go \
	system.go \
	transform.go \

DATA = \
	xterm.css \
	xterm.js \
	xterm-addon-fit.js \
	xterm.html

CMD_DATA=\
	cmd/rsa.key \
	cmd/tls.crt \
	cmd/tls.key

CMD_SRCS=\
	cmd/config.go \
	cmd/logger.go \
	cmd/main.go

all: $(NAME)

$(NAME): $(DATA) $(SRCS) $(CMD_DATA) $(CMD_SRCS)
	CGO_ENABLED=0 go build -x -ldflags "-X 'main.HODU_NAME=$(NAME)' -X 'main.HODU_VERSION=$(VERSION)'" -o $@ $(CMD_SRCS)
	##CGO_ENABLED=1 go build -x -ldflags "-X 'main.HODU_NAME=$(NAME)' -X 'main.HODU_VERSION=$(VERSION)'" -o $@ $(CMD_SRCS)
	##CGO_ENABLED=1 go build -x -ldflags "-X 'main.HODU_NAME=$(NAME)' -X 'main.HODU_VERSION=$(VERSION)' -linkmode external -extldflags=-static" -o $@ $(CMD_SRCS)

$(NAME).debug: $(DATA) $(SRCS) $(CMD_DATA) $(CMD_SRCS)
	CGO_ENABLED=1 go build -race -x -ldflags "-X 'main.HODU_NAME=$(NAME)' -X 'main.HODU_VERSION=$(VERSION)'" -o $@ $(CMD_SRCS)

clean:
	go clean -x -i
	rm -f $(NAME) $(NAME).debug

check:
	go test -x

hodu.pb.go: hodu.proto
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		hodu.proto

hodu_grpc.pb.go: hodu.proto
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		hodu.proto

xterm.js:
	curl -L -o "$@" https://cdn.jsdelivr.net/npm/@xterm/xterm/lib/xterm.min.js

xterm-addon-fit.js:
	curl -L -o "$@" https://cdn.jsdelivr.net/npm/xterm-addon-fit/lib/xterm-addon-fit.min.js

xterm.css:
	curl -L -o "$@" https://cdn.jsdelivr.net/npm/@xterm/xterm/css/xterm.min.css
	sed -r -i 's|^/\*# sourceMappingURL=/.+ \*/$$||g' "$@"

cmd/tls.crt:
	openssl req -x509 -newkey rsa:4096 -keyout cmd/tls.key -out cmd/tls.crt -sha256 -days 36500 -nodes -subj "/CN=$(NAME)" --addext "subjectAltName=DNS:$(NAME),IP:127.0.0.1,IP:::1"

cmd/tls.key:
	openssl req -x509 -newkey rsa:4096 -keyout cmd/tls.key -out cmd/tls.crt -sha256 -days 36500 -nodes -subj "/CN=$(NAME)" --addext "subjectAltName=DNS:$(NAME),IP:127.0.0.1,IP:::1"

cmd/rsa.key:
	openssl genrsa -traditional -out cmd/rsa.key 2048

.PHONY: all clean test
