BINARY  := bin/faztudo
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build run dev test cover vet fmt lint clean docker desktop-dev desktop-mac install-mac desktop-windows desktop-linux dmg

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
	rm -rf bin coverage.out build/bin build/wailsjs dist

docker: ## Imagem de container (modo simulação)
	docker build --build-arg VERSION=$(VERSION) -t faztudo:$(VERSION) .

# App desktop (Wails). Instale o CLI com:
#   go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0
WAILS        ?= $(or $(shell command -v wails 2>/dev/null),$(HOME)/go/bin/wails)
APP_VERSION  ?= $(or $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//'),0.1.0)
WAILS_TAGS   := desktop
WAILS_FLAGS   = -clean -skipbindings -trimpath -tags "$(WAILS_TAGS)" -ldflags "-X main.version=$(VERSION)"

desktop-dev: ## Janela desktop com recarga dos assets
	$(WAILS) dev -skipbindings -tags "$(WAILS_TAGS)"

desktop-mac: ## FazTudo.app universal (Intel + Apple Silicon) em build/bin
	MACOSX_DEPLOYMENT_TARGET=13.0 $(WAILS) build $(WAILS_FLAGS) -platform darwin/universal

LSREGISTER := /System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister

install-mac: desktop-mac ## Compila e substitui /Applications/FazTudo.app (feche o app antes)
	rm -rf /Applications/FazTudo.app
	ditto build/bin/FazTudo.app /Applications/FazTudo.app
	$(LSREGISTER) -u build/bin/FazTudo.app 2>/dev/null || true
	rm -rf build/bin/FazTudo.app
	@echo "Instalado em /Applications/FazTudo.app"

dmg: desktop-mac ## Instalador .dmg em dist/
	./scripts/package-dmg.sh $(APP_VERSION)

desktop-windows: ## Instalador NSIS (.exe) em build/bin; requer makensis
	$(WAILS) build $(WAILS_FLAGS) -platform windows/amd64 -nsis -webview2 download

desktop-linux: WAILS_TAGS := desktop webkit2_41
desktop-linux: ## Binário Linux (rodar no Linux; requer libgtk-3-dev e libwebkit2gtk-4.1-dev)
	$(WAILS) build $(WAILS_FLAGS) -platform linux/amd64
