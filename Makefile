SHELL := /bin/bash

BIN := runtime/bin/teemohub
PID := runtime/teemohub.pid
LOG := runtime/logs/teemohub.log

.PHONY: help newapi bootstrap web build up down restart e2e limits persist audit reconcile status

help:
	@printf '%s\n' \
		'make newapi     构建并启动本地 New API (:3000)' \
		'make bootstrap  初始化 New API root、渠道与 teemoHub Token' \
		'make web        构建前端（web/dist）' \
		'make build      编译 teemoHub' \
		'make up         启动 teemoHub (:18080/:18081)' \
		'make down       停止 teemoHub' \
		'make restart    重启 teemoHub' \
		'make e2e        运行端到端测试（真实上游）' \
		'make limits     并发上限与 RPM 限流验证' \
		'make persist    重启后计费与账本一致性验证' \
		'make audit      价格版本化与审计不可篡改验证' \
		'make reconcile  与 New API 日志做按日对账' \
		'make status     查看两个服务的存活状态'

newapi:
	./scripts/newapi-up.sh

bootstrap:
	./scripts/bootstrap-newapi.sh

web:
	cd web && npm run build

build:
	go build -o $(BIN) ./cmd/teemohub

up: build
	@mkdir -p runtime/bin runtime/logs
	@if [ -f $(PID) ] && kill -0 "$$(cat $(PID))" 2>/dev/null; then \
		echo "teemoHub already running pid $$(cat $(PID))"; \
	else \
		nohup ./$(BIN) >> $(LOG) 2>&1 & echo $$! > $(PID); \
		sleep 1; echo "teemoHub started pid $$(cat $(PID)) -> http://127.0.0.1:18080"; \
	fi

down:
	@if [ -f $(PID) ]; then \
		kill "$$(cat $(PID))" 2>/dev/null || true; rm -f $(PID); echo "teemoHub stopped"; \
	else echo "teemoHub not running"; fi

restart: down up

e2e:
	python3 scripts/e2e.py

limits:
	python3 scripts/limits.py

persist:
	python3 scripts/persistence.py

audit:
	python3 scripts/audit.py

reconcile:
	python3 scripts/reconcile_daily.py --days 7

status:
	@curl -s -o /dev/null -w 'control %{http_code}\n' http://127.0.0.1:18080/api/models || true
	@curl -s -o /dev/null -w 'newapi  %{http_code}\n' http://127.0.0.1:3000/api/status || true
