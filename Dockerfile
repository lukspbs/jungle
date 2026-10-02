# Build e execução do serviço.
#
# A versão do Go é a mesma declarada no go.mod. Fixá-la aqui evita que uma
# imagem mais nova mude o comportamento do binário entre a máquina de quem
# desenvolve e a de quem avalia.
FROM golang:1.27.1-alpine AS build

WORKDIR /src

# As dependências entram antes do código: enquanto go.mod e go.sum não mudarem,
# esta camada é reaproveitada e o download não se repete a cada build.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO desligado produz um binário estático, que roda numa imagem sem libc.
# -trimpath remove caminhos da máquina de build do binário final.
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/api  ./cmd/api && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# A imagem final não tem shell nem gerenciador de pacotes: a superfície de
# ataque é o binário e mais nada.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/api     /usr/local/bin/api
COPY --from=build /out/migrate /usr/local/bin/migrate

# Usuário sem privilégios, já definido pela imagem base.
USER nonroot:nonroot

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
