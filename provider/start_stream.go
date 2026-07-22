package provider

import (
	"context"
	"errors"
	"io"
)

// StartStream creates a runtime stream and starts consuming provider chunks.
func StartStream[Request any, Chunk any](
	ctx context.Context,
	p Provider[Request, Chunk],
	request Request,
) (*Stream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := p.Stream(ctx, request)
	if err != nil {
		return nil, err
	}

	stream := newStream()
	go consume(ctx, stream, raw, p)
	return stream, nil
}

func consume[Request any, Chunk any](
	ctx context.Context,
	stream *Stream,
	raw ChunkStream[Chunk],
	p Provider[Request, Chunk],
) {
	defer raw.Close()

	for {
		select {
		case <-ctx.Done():
			stream.fail(ctx.Err())
			return
		default:
		}

		chunk, err := raw.Recv()
		if errors.Is(err, io.EOF) {
			if completeErr := stream.complete(StreamEvent{
				Type:         StreamCompleted,
				FinishReason: "eof",
			}); completeErr != nil {
				stream.fail(completeErr)
			}
			return
		}
		if err != nil {
			stream.fail(err)
			return
		}

		events, err := p.Parse(chunk)
		if err != nil {
			stream.fail(err)
			return
		}

		for _, event := range events {
			if event.Type == StreamCompleted {
				if completeErr := stream.complete(event); completeErr != nil {
					stream.fail(completeErr)
				}
				return
			}
			stream.publish(event)
		}
	}
}
