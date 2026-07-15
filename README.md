# herrscher-codex-backend

Backend Codex pour Herrscher. Ce module implémente le même contrat que
`herrscher-claude-backend`, mais parle au CLI Codex local.

## Entrée

```go
func NewBackend(ctx context.Context, c Config) (contracts.Backend, error)
```

La configuration principale est :

```go
type Config struct {
    Kind   string // "stream" | "oneshot"
    Stream bool   // utilisé si Kind est vide
    Cmd    string // commande de base, défaut pratique : codex
    Model  string
    Effort string
    Dir    string
}
```

## Modes

Le mode `stream` démarre un processus persistant :

```text
codex app-server --listen stdio://
```

Le backend initialise la connexion JSONL, crée ou reprend un thread, puis envoie
un `turn/start` par message. Les notifications `item/agentMessage/delta`, les
exécutions de commandes et `turn/completed` sont converties en événements
`contracts.BackendEvent`. Une mort du processus déclenche `reset`, un redémarrage
et une nouvelle tentative du tour.

Le mode `oneshot` exécute `codex exec --json` à chaque message. Il conserve les
variables `DCTL_MSG`, `DCTL_AUTHOR`, `DCTL_MESSAGE_ID`, `DCTL_CHANNEL` et
`DCTL_ATTACHMENTS` pour les intégrations qui en ont besoin.

## Enregistrement

Un blank import auto-enregistre le plugin avec `Kind: "codex"`. Les réglages
disponibles sont `CODEX_CMD`, `CODEX_MODEL`, `CODEX_EFFORT`, `CODEX_STREAM`,
`CODEX_DIR` et `CODEX_KIND`.

`CommandPresets("codex")` expose la matrice modèle × effort pour les suggestions
de `/session create cmd:`.

## Développement

Ce module est hors du `go.work` parent ; les commandes locales utilisent donc :

```bash
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test ./...
```

Le test live persistant est ignoré par défaut. Pour l’exécuter avec une
installation Codex authentifiée :

```bash
DCTL_LIVE=1 GOWORK=off go test -run Live ./...
```
