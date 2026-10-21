BINARY  := bin/faztudo
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build run dev test cover vet fmt lint clean docker

all: fmt vet test build

build: ## Compila o binário em bin/
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

run: build ## Compila e executa em modo simulação
	./$(BINARY)

dev: ## Executa direto do código-fonte
	go run . -log-level debug

test: ## Testes com detector de race
	go test -race ./...

cover: ## Relatório de cobertura
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

vet:
	go vet ./...

fmt:
	gofmt -s -w .

lint: vet ## Verifica formatação e análise estática
	@test -z "$$(gofmt -l .)" || (echo "Arquivos sem gofmt:"; gofmt -l .; exit 1)

clean:
	rm -rf bin coverage.out

docker: ## Imagem de container (modo simulação)
	docker build --build-arg VERSION=$(VERSION) -t faztudo:$(VERSION) .
