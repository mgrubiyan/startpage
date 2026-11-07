# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/startpage ./cmd/startpage

# distroless/static: CA certificates and tzdata, no shell, runs as nonroot.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/startpage /startpage
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 CMD ["/startpage", "healthcheck"]
ENTRYPOINT ["/startpage"]
