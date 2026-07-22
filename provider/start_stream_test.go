package provider

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type fakeRequest struct{}

type fakeProvider struct {
	streamErr error
	parseErr  error
	chunks    []fakeChunk
}

type fakeChunk struct {
	events []StreamEvent
	err    error
}

func (p fakeProvider) Stream(context.Context, fakeRequest) (ChunkStream[fakeChunk], error) {
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	return &fakeChunkStream{chunks: p.chunks}, nil
}

func (p fakeProvider) Parse(chunk fakeChunk) ([]StreamEvent, error) {
	if p.parseErr != nil {
		return nil, p.parseErr
	}
	return chunk.events, chunk.err
}

type fakeChunkStream struct {
	chunks []fakeChunk
	index  int
	closed bool
}

func (s *fakeChunkStream) Recv() (fakeChunk, error) {
	if s.index >= len(s.chunks) {
		return fakeChunk{}, io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}

func (s *fakeChunkStream) Close() error {
	s.closed = true
	return nil
}

func TestStartStreamRunsGenericProviderAndClosesSubscription(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p := fakeProvider{chunks: []fakeChunk{
		{events: []StreamEvent{{Type: ContentReceived, Content: "hello"}}},
		{events: []StreamEvent{{Type: StreamCompleted}}},
	}}

	stream, err := StartStream(ctx, p, fakeRequest{})
	if err != nil {
		t.Fatalf("StartStream: %v", err)
	}
	events := collectEvents(t, stream.Subscribe(ctx))
	if len(events) != 2 || events[0].Content != "hello" || events[1].Type != StreamCompleted {
		t.Fatalf("events = %#v", events)
	}
	result, err := stream.Result()
	if err != nil || result.Content != "hello" || !result.Finished {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestStartStreamReturnsSetupErrorSynchronously(t *testing.T) {
	want := errors.New("setup failed")
	_, err := StartStream(context.Background(), fakeProvider{streamErr: want}, fakeRequest{})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestStartStreamPublishesParseErrorAndCloses(t *testing.T) {
	want := errors.New("parse failed")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := StartStream(ctx, fakeProvider{parseErr: want, chunks: []fakeChunk{{}}}, fakeRequest{})
	if err != nil {
		t.Fatalf("StartStream: %v", err)
	}
	events := collectEvents(t, stream.Subscribe(ctx))
	if len(events) != 1 || events[0].Type != StreamFailed {
		t.Fatalf("events = %#v", events)
	}
	if _, err := stream.Result(); !errors.Is(err, want) {
		t.Fatalf("Result error = %v, want %v", err, want)
	}
}
