// Package orchestrator holds the provider-agnostic core (state resolver,
// context builder, phase dispatch). It imports only the board and forge
// ports — never a provider implementation. (Built out in M1+.)
package orchestrator
