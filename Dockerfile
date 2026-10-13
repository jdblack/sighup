# syntax=docker/dockerfile:1

# ---------- build ----------
FROM golang:1.27 AS build

WORKDIR /src

# Dependencies first, so a source-only change does not refetch the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off for a static binary; -trimpath and -s -w keep the artifact small.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sighup .

# ---------- runtime ----------
# distroless/static carries CA certificates (needed for HTTPS to Harbor) and no shell: the binary
# is the entire attack surface.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/sighup /usr/local/bin/sighup

EXPOSE 8080
USER nonroot

ENTRYPOINT ["/usr/local/bin/sighup"]
CMD ["serve"]
