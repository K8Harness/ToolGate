.PHONY: demo demo-resilience

demo:
	EVAL_COMPOSE_FILE=deploy/docker-compose.yml \
	POSTGRES_DSN=postgres://gateway:gateway@127.0.0.1:15432/gateway?sslmode=disable \
	AGENT_URL=http://127.0.0.1:18085 \
	go run ./cmd/eval-runner evalsuite/default.yaml

demo-resilience:
	@bash scripts/demo-resilience.sh
