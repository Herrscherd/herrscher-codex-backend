// Package codex is the Herrscher backend for the Codex agent. A stream session
// drives `codex app-server` over stdio, and a one-shot turn runs `codex exec`.
// Approval requests go to the approve binary. The host imports the package for
// the registration its init function makes, and reads the models from Models.
package codex
