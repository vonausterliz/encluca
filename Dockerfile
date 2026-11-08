# Immagine dell'hub encluca per TrueNAS (o qualunque host Docker).
# Fase 1: compilazione statica dal sorgente, per l'architettura di destinazione.
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY . .
# modulo WebAssembly per sfogliare la cassaforte dal browser (incluso nel binario)
RUN GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w" -o web/vault.wasm ./cmd/vaultwasm && \
    cp "$(go env GOROOT)/misc/wasm/wasm_exec.js" web/wasm_exec.js
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/encluca .

# Fase 2: immagine minima. ca-certificates serve per Google Drive, tzdata per l'ora locale nel registro.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/encluca /app/encluca
LABEL org.opencontainers.image.source="https://github.com/vonausterliz/encluca" \
      org.opencontainers.image.description="encluca hub: cartella cifrata sincronizzata tramite il NAS"
USER 568:568
EXPOSE 8443
VOLUME ["/data"]
ENTRYPOINT ["/app/encluca"]
CMD ["hub", "--data", "/data", "--listen", ":8443"]
