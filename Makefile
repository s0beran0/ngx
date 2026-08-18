# Alvos do ngx.
#
# A bancada de teste (test/bancada) e um container descartavel com sshd e
# nginx, usado pelos testes de integracao do caminho remoto.

SHELL := /bin/bash

BANCADA_DIR   := test/bancada
BANCADA_IMG   := ngx-bancada
BANCADA_CT    := ngx-bancada
# Porta alta e fixa no host: os testes de integracao sabem onde conectar.
BANCADA_PORTA ?= 2222
BANCADA_CHAVE := $(BANCADA_DIR)/.chave/id_ed25519

.PHONY: bancada-up bancada-down bancada-smoke bancada-logs bancada-shell

# A chave de teste e gerada, nunca commitada.
$(BANCADA_CHAVE):
	@mkdir -p $(dir $(BANCADA_CHAVE))
	@ssh-keygen -t ed25519 -N '' -C ngx-bancada -f $(BANCADA_CHAVE) >/dev/null
	@echo "bancada: chave de teste gerada em $(BANCADA_CHAVE)"

bancada-up: $(BANCADA_CHAVE)
	@echo "bancada: construindo a imagem..."
	@docker build -t $(BANCADA_IMG) $(BANCADA_DIR)
	@docker rm -f $(BANCADA_CT) >/dev/null 2>&1 || true
	@docker create --name $(BANCADA_CT) \
		-p 127.0.0.1:$(BANCADA_PORTA):22 \
		$(BANCADA_IMG) >/dev/null
	@docker cp $(BANCADA_CHAVE).pub $(BANCADA_CT):/chave-publica.pub
	@docker start $(BANCADA_CT) >/dev/null
	@echo "bancada: esperando o sshd..."
	@for i in $$(seq 1 60); do \
		if ssh -i $(BANCADA_CHAVE) -p $(BANCADA_PORTA) \
			-o IdentitiesOnly=yes -o BatchMode=yes \
			-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
			-o LogLevel=ERROR -o ConnectTimeout=3 \
			ngxtest@127.0.0.1 true 2>/dev/null; then \
			echo "bancada: no ar em 127.0.0.1:$(BANCADA_PORTA) (usuario ngxtest)"; \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "bancada: sshd nao respondeu; veja 'make bancada-logs'" >&2; \
	exit 1

bancada-smoke:
	@$(BANCADA_DIR)/smoke.sh $(BANCADA_PORTA) $(BANCADA_CHAVE)

bancada-down:
	@docker rm -f $(BANCADA_CT) >/dev/null 2>&1 || true
	@docker rmi -f $(BANCADA_IMG) >/dev/null 2>&1 || true
	@rm -rf $(dir $(BANCADA_CHAVE))
	@echo "bancada: container, imagem e chave de teste removidos"

bancada-logs:
	@docker logs $(BANCADA_CT)

bancada-shell:
	@ssh -i $(BANCADA_CHAVE) -p $(BANCADA_PORTA) \
		-o IdentitiesOnly=yes \
		-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
		-o LogLevel=ERROR ngxtest@127.0.0.1

.PHONY: ajuda build test test-race fuzz cover fmt lint verificar limpar

# `make` sem argumento lista o que da para fazer, em vez de rodar algo por
# engano.
.DEFAULT_GOAL := ajuda

ajuda:
	@echo "alvos do ngx:"
	@echo "  build         compila o binario em bin/ngx"
	@echo "  test          testes, sem -race (rapido)"
	@echo "  test-race     testes com detector de corrida"
	@echo "  fuzz          fuzz do alinhamento por FUZZTIME (default 60s)"
	@echo "  cover         cobertura em cover.html"
	@echo "  fmt           aplica gofmt"
	@echo "  lint          gofmt -l e go vet, sem alterar arquivo"
	@echo "  verificar     lint + test-race + fuzz curto; o que o CI roda"
	@echo "  bancada-up    sobe o container de teste (sshd + nginx)"
	@echo "  bancada-smoke prova as propriedades da bancada"
	@echo "  bancada-down  derruba e limpa a bancada"
	@echo "  limpar        remove bin/ e artefatos de cobertura"

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -o bin/ngx ./cmd/ngx

test:
	go test ./...

test-race:
	go test ./... -race

FUZZTIME ?= 60s
fuzz:
	go test ./internal/config/ -run 'XXX_nenhum' -fuzz FuzzAlinhamento -fuzztime $(FUZZTIME)

cover:
	go test ./... -coverprofile=cover.out
	go tool cover -html=cover.out -o cover.html
	@echo "cobertura em cover.html"

fmt:
	gofmt -w .

# lint nao altera arquivo: serve para o CI e para conferir antes de commitar.
lint:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "arquivos fora do gofmt acima" >&2; exit 1)
	go vet ./...

verificar: lint test-race
	@$(MAKE) fuzz FUZZTIME=30s

limpar:
	rm -rf bin cover.out cover.html
