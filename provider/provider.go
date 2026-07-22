package provider

import "context"

// ChunkStream is the provider-neutral raw streaming input.
type ChunkStream[Chunk any] interface {
	Recv() (Chunk, error)
	Close() error
}

// Provider adapts a provider-specific request and raw chunk type to generic
// stream events.
type Provider[Request any, Chunk any] interface {
	Stream(context.Context, Request) (ChunkStream[Chunk], error)
	Parse(Chunk) ([]StreamEvent, error)
}
