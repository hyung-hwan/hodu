all: hodu.pb.go hodu_grpc.pb.go
	go build -x -o hodu cmd/main.go


hodu.pb.go: hodu.proto
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		hodu.proto

hodu_grpc.pb.go: hodu.proto
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		hodu.proto
