# syntax=docker/dockerfile:1

# 1. Build the React UI into internal/web/dist.
FROM node:20-alpine AS web
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY index.html tsconfig.json tsconfig.app.json vite.config.ts ./
COPY src ./src
COPY internal/web/dist/.gitkeep internal/web/dist/.gitkeep
RUN npm run build

# 2. Build one static Go binary that embeds the UI.
FROM golang:1.23-alpine AS api
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=web /src/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tradecore ./cmd/tradecore

# 3. Run it on a minimal, non-root base image.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=api /out/tradecore /tradecore
ENV APP_ENV=production TRUST_PROXY=true
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/tradecore"]
