# syntax=docker/dockerfile:1

# ---------- test ----------
# The tests run here, not in a CI step. The runner executes jobs in a container on the DinD daemon,
# and a step cannot bind-mount the job's working directory into a sibling container -- `docker run
# -v "$PWD":/src` resolves $PWD on the *daemon's* filesystem (the pod), which is empty, so the test
# command dies instantly. The job image also carries no Go toolchain. Building the image therefore
# proves the tests pass.
FROM golang:1.27 AS test

WORKDIR /src

# Dependencies first, so a source-only change does not refetch the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go test ./...

# ---------- build ----------
FROM test AS build

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
