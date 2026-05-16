# Build from the repository root so that go.mod/go.sum are included:
#   docker build -t fake-slack -f examples/fake-mcp-servers/slack/Dockerfile .

# Stage 1: Build
FROM golang:1.25-alpine AS builder

WORKDIR /build

# Copy go module files from the repo root and download dependencies.
COPY go.mod go.sum ./
RUN go mod download

# Copy the full source tree (needed because the module root is the repo root).
COPY . .

# Build the fake-slack binary.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -o /fake-slack \
    ./examples/fake-mcp-servers/slack/

# Stage 2: Minimal runtime image
FROM alpine:latest

COPY --from=builder /fake-slack /fake-slack

EXPOSE 8084

ENTRYPOINT ["/fake-slack"]
